package ghauthcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/ghauth"
	"github.com/onelastcommit/noctra/internal/github"
)

const maxSafeAgentTimeout = 50 * time.Minute

func Run(scriptDir string, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	cfg, err := config.Load(scriptDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "login":
		return login(ctx, cfg, os.Stdout, hasFlag(args[1:], "--force"))
	case "logout":
		return logout(ctx, cfg, os.Stdout, hasFlag(args[1:], "--force"))
	case "status":
		return status(ctx, cfg, os.Stdout, flagValue(args[1:], "--repo"))
	case "help", "--help", "-h":
		printUsage()
		return nil
	}
	printUsage()
	return fmt.Errorf("unknown github subcommand %q", args[0])
}

func printUsage() {
	fmt.Println("Usage: noctra github <command>")
	fmt.Println()
	fmt.Println("  login [--force]      Link this machine to the noctra-agent GitHub App (device flow)")
	fmt.Println("  logout [--force]     Unlink this machine and delete its key")
	fmt.Println("  status [--repo o/n]  Show the GitHub auth mode; with --repo, test a read token for that repository")
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
	}
	return ""
}

func login(ctx context.Context, cfg *config.Config, out io.Writer, force bool) error {
	dir := cfg.GitHubAuthDir
	if ghauth.IsLinked(dir) {
		if !force {
			inst, _, err := ghauth.Load(dir)
			if err == nil {
				return fmt.Errorf("already linked as %s (instance %s); run `noctra github logout` first, or `noctra github login --force` to replace it", inst.GitHubLogin, inst.InstanceID)
			}
			return fmt.Errorf("an existing link in %s could not be read (%v); run `noctra github login --force` to replace it", dir, err)
		}
		if err := logout(ctx, cfg, out, true); err != nil {
			return err
		}
	}

	svc := ghauth.NewService(cfg.AuthServiceURL)
	fmt.Fprintf(out, "→ Token service: %s\n", cfg.AuthServiceURL)
	app, err := svc.Config(ctx)
	if err != nil {
		return fmt.Errorf("reach token service: %w", err)
	}

	flow := ghauth.NewDeviceFlow(app.ClientID)
	code, err := flow.Start(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n→ Open %s and enter this code: %s\n\n", code.VerificationURI, code.UserCode)
	fmt.Fprintln(out, "  Waiting for you to approve it in the browser…")
	userToken, err := flow.Wait(ctx, code)
	if err != nil {
		return err
	}

	_, key, err := ghauth.NewKey()
	if err != nil {
		return fmt.Errorf("generate instance key: %w", err)
	}
	res, err := svc.Link(ctx, userToken, key)
	if err != nil {
		var se *ghauth.ServiceError
		if errors.As(err, &se) && se.Code == "no_installations" {
			return fmt.Errorf("%s is not installed on any account you can access yet. Install it on the repositories Noctra should work on (%s), then run `noctra github login` again", app.Slug, app.InstallURL)
		}
		return err
	}

	inst := ghauth.Instance{
		InstanceID:    res.InstanceID,
		ServiceURL:    cfg.AuthServiceURL,
		GitHubUserID:  res.GitHubUser.ID,
		GitHubLogin:   res.GitHubUser.Login,
		AppSlug:       res.App.Slug,
		BotLogin:      res.App.Bot.Login,
		BotUserID:     res.App.Bot.ID,
		BotEmail:      res.App.Bot.Email,
		Installations: res.Installations,
		LinkedAt:      time.Now().UTC(),
	}
	if err := ghauth.Save(dir, inst, key); err != nil {
		if uerr := svc.Unlink(ctx, inst.InstanceID, key); uerr != nil {
			slog.Warn("github login: could not roll back the link after a save failure", "err", uerr)
		}
		return err
	}

	fmt.Fprintf(out, "\n✅ Linked as %s (instance %s)\n", inst.GitHubLogin, inst.InstanceID)
	fmt.Fprintf(out, "   Installations: %s\n", installationList(inst.Installations))
	fmt.Fprintf(out, "   Commits will be authored as %s <%s>\n", inst.BotLogin, inst.BotEmail)
	fmt.Fprintf(out, "   Key stored in %s (mode 0600)\n", dir)
	if cfg.GitHubAuthMode == "token" {
		fmt.Fprintln(out, "\n⚠️  GITHUB_AUTH_MODE=token is set, so Noctra keeps using your personal credentials until you change it to auto or app.")
	} else {
		fmt.Fprintln(out, "\n   Restart Noctra to switch to the app (e.g. `noctra restart`).")
	}
	return nil
}

func installationList(insts []ghauth.Installation) string {
	if len(insts) == 0 {
		return "none"
	}
	names := make([]string, 0, len(insts))
	for _, i := range insts {
		names = append(names, i.Account)
	}
	return strings.Join(names, ", ")
}

func logout(ctx context.Context, cfg *config.Config, out io.Writer, force bool) error {
	dir := cfg.GitHubAuthDir
	inst, key, err := ghauth.Load(dir)
	if errors.Is(err, ghauth.ErrNotLinked) {
		fmt.Fprintln(out, "Not linked; nothing to do.")
		return nil
	}
	if err != nil && !force {
		return fmt.Errorf("%w (use --force to delete the local link anyway)", err)
	}
	if err == nil {
		uerr := ghauth.NewService(inst.ServiceURL).Unlink(ctx, inst.InstanceID, key)
		switch {
		case uerr == nil:
			fmt.Fprintf(out, "→ Unlinked instance %s from %s\n", inst.InstanceID, inst.ServiceURL)
		case ghauth.IsServiceError(uerr, "unknown_instance"):
			fmt.Fprintln(out, "→ The token service had already forgotten this instance")
		case force:
			fmt.Fprintf(out, "⚠️  Could not unlink on the token service (%v); deleting the local link anyway\n", uerr)
		default:
			return fmt.Errorf("%w (use --force to delete the local link anyway)", uerr)
		}
	}
	if err := ghauth.Remove(dir); err != nil {
		return err
	}
	fmt.Fprintln(out, "✅ Local key deleted. Noctra falls back to personal GitHub credentials on its next start.")
	return nil
}

func status(ctx context.Context, cfg *config.Config, out io.Writer, repo string) error {
	linked := ghauth.IsLinked(cfg.GitHubAuthDir)
	mode, err := ghauth.ResolveMode(cfg.GitHubAuthMode, linked)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Mode: %s (GITHUB_AUTH_MODE=%s)\n", mode, cfg.GitHubAuthMode)
	if !linked {
		fmt.Fprintln(out, "Not linked. Run `noctra github login` to use the noctra-agent GitHub App.")
		return nil
	}
	sess, err := ghauth.OpenSession(cfg.GitHubAuthDir, "")
	if err != nil {
		return err
	}
	inst := sess.Instance
	fmt.Fprintf(out, "Linked as: %s (instance %s, since %s)\n", inst.GitHubLogin, inst.InstanceID, inst.LinkedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "Installations: %s\n", installationList(inst.Installations))
	fmt.Fprintf(out, "Commit identity: %s <%s>\n", inst.BotLogin, inst.BotEmail)
	fmt.Fprintf(out, "Token service: %s", inst.ServiceURL)
	if err := ghauth.NewService(inst.ServiceURL).Health(ctx); err != nil {
		fmt.Fprintf(out, " (unreachable: %v)\n", err)
	} else {
		fmt.Fprintln(out, " (reachable)")
	}
	if repo == "" {
		return nil
	}
	tok, err := sess.FreshToken(ctx, repo, ghauth.ScopeRead)
	if err != nil {
		return fmt.Errorf("test token for %s: %w", repo, err)
	}
	fmt.Fprintf(out, "Test token for %s: OK (read scope, expires %s)\n", repo, tok.ExpiresAt.Local().Format("15:04"))
	return nil
}

func Activate(cfg *config.Config) (*ghauth.Session, error) {
	mode, err := ghauth.ResolveMode(cfg.GitHubAuthMode, ghauth.IsLinked(cfg.GitHubAuthDir))
	if err != nil {
		return nil, err
	}
	if mode != ghauth.ModeApp {
		return nil, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate noctra binary for the git credential helper: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	sess, err := ghauth.OpenSession(cfg.GitHubAuthDir, exe)
	if err != nil {
		return nil, err
	}
	if err := sess.Activate(); err != nil {
		return nil, err
	}
	github.SetTokenSource(sess.GHToken)
	ghauth.SetActive(sess)
	if cfg.AgentTimeout > maxSafeAgentTimeout {
		slog.Warn("github app: AGENT_TIMEOUT_MINUTES exceeds 50; an agent's GH_TOKEN (valid one hour) may expire before the run ends",
			"timeout", cfg.AgentTimeout)
	}
	return sess, nil
}
