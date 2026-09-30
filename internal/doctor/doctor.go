package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/linear"
)

type check struct {
	name   string
	ok     bool
	detail string
	hint   string
}

func gather(scriptDir string) []check {
	var checks []check

	cfg, loadErr := config.Load(scriptDir)

	var clis []string
	if loadErr == nil {
		clis = cfg.RequiredCLIs()
	} else {
		clis = []string{"git", "gh", config.AgentCLIs()[config.DefaultAgentBackend]}
	}
	for _, cli := range clis {
		if loadErr == nil && cli == cfg.AgentCLI() {
			if _, valid := config.AgentCLIs()[cfg.AgentBackend]; valid {
				checks = append(checks, check{
					name:   "agent backend",
					ok:     true,
					detail: fmt.Sprintf("%s (%s)", cfg.AgentBackend, cfg.AgentCLI()),
				})
			} else {
				checks = append(checks, check{
					name:   "agent backend",
					ok:     false,
					detail: fmt.Sprintf("unsupported backend %q", cfg.AgentBackend),
					hint:   "AGENT_BACKEND must be \"claude\", \"codex\", \"copilot\", or \"antigravity\"",
				})
			}
		}
		checks = append(checks, checkCLI(cli))
	}

	if loadErr == nil {
		defaultCLI := cfg.AgentCLI()
		for _, cli := range cfg.AllCandidateCLIs() {
			if cli == defaultCLI || cli == "git" || cli == "gh" {
				continue
			}
			c := checkCLI(cli)
			if !c.ok {
				c.ok = true
				c.detail = "not installed (optional — needed if a ticket uses agent:" + cli + " label)"
			}
			checks = append(checks, c)
		}
	}

	checks = append(checks, checkGHAuth())

	if loadErr != nil {
		checks = append(checks, check{
			name:   "config",
			detail: loadErr.Error(),
			hint:   "Run `noctra setup` to generate config files.",
		})
	} else {
		checks = append(checks, checkLinearKey(cfg))
		checks = append(checks, checkRepos(cfg))
		checks = append(checks, checkDashboard(cfg))
	}

	checks = append(checks, check{
		name:   "config dir",
		ok:     true,
		detail: scriptDir,
	})

	return checks
}

func Run(scriptDir string) error {
	fmt.Println("Checking dependencies and configuration...")
	fmt.Println()

	checks := gather(scriptDir)

	passed, failed := 0, 0
	for _, c := range checks {
		if c.ok {
			passed++
			fmt.Printf("  ✓ %-16s %s\n", c.name, c.detail)
		} else {
			failed++
			fmt.Printf("  ✗ %-16s %s\n", c.name, c.detail)
			if c.hint != "" {
				fmt.Printf("    %-16s %s\n", "", c.hint)
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("  %d passed, %d failed\n", passed, failed)
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Printf("  All %d checks passed — ready to roll.\n", passed)
	return nil
}

type jsonCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

func RunJSON(scriptDir string, w io.Writer) error {
	checks := gather(scriptDir)

	out := make([]jsonCheck, 0, len(checks))
	failed := 0
	for _, c := range checks {
		if !c.ok {
			failed++
		}
		out = append(out, jsonCheck{Name: c.name, OK: c.ok, Detail: c.detail, Hint: c.hint})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

func checkCLI(name string) check {
	path, err := exec.LookPath(name)
	if err != nil {
		hints := map[string]string{
			"git":     "Install git: https://git-scm.com/downloads",
			"gh":      "Install GitHub CLI: https://cli.github.com",
			"claude":  "Install Claude Code: https://docs.anthropic.com/en/docs/claude-code",
			"codex":   "Install Codex CLI: npm i -g @openai/codex, then run `codex login`",
			"copilot": "Install Copilot CLI: npm i -g @github/copilot (authenticates via `gh auth login` / GH_TOKEN)",
			"agy":     "Install Antigravity CLI (`agy`), then run `agy` once to log in (Google AI Pro): https://antigravity.google",
		}
		return check{
			name:   name,
			detail: "not found in PATH",
			hint:   hints[name],
		}
	}
	return check{name: name, ok: true, detail: path}
}

func checkGHAuth() check {
	if _, err := exec.LookPath("gh"); err != nil {
		return check{
			name:   "gh auth",
			detail: "skipped (gh not installed)",
		}
	}

	out, err := exec.Command("gh", "auth", "status").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return check{
			name:   "gh auth",
			detail: detail,
			hint:   "Run `gh auth login` to authenticate.",
		}
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Logged in") {
			return check{name: "gh auth", ok: true, detail: strings.TrimSpace(line)}
		}
		if strings.Contains(line, "account") {
			return check{name: "gh auth", ok: true, detail: strings.TrimSpace(line)}
		}
	}
	return check{name: "gh auth", ok: true, detail: "authenticated"}
}

func checkLinearKey(cfg *config.Config) check {
	name := "LINEAR_API_KEY"
	var client *linear.Client
	var isOAuth bool
	switch {
	case cfg.ActorAppConfigured():
		name = "LINEAR_OAUTH (actor=app)"
		tm := linear.NewTokenManager(linear.TokenManagerConfig{
			ClientID:     cfg.LinearOAuthClientID,
			ClientSecret: cfg.LinearOAuthClientSecret,
			RefreshToken: cfg.LinearOAuthRefreshToken,
			Scope:        cfg.LinearOAuthScope,
		})
		client = linear.New(cfg.LinearAPIKey)
		client.TokenFn = tm.Token
		isOAuth = true
	case cfg.LinearOAuthToken != "":
		name = "LINEAR_OAUTH_TOKEN"
		client = linear.NewOAuth(cfg.LinearOAuthToken)
		isOAuth = true
	case cfg.LinearAPIKey != "":
		client = linear.New(cfg.LinearAPIKey)
	default:
		return check{
			name:   name,
			detail: "not set",
			hint:   "Run `noctra setup` or set LINEAR_API_KEY (or LINEAR_OAUTH_TOKEN) in .env.",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	who, err := client.Ping(ctx)
	if err != nil {
		hint := "Check your credential in .env or at https://linear.app/settings/api."
		if isOAuth {
			hint = "Check your OAuth token in .env or create/revoke tokens at https://linear.app/settings/api."
		}
		return check{
			name:   name,
			detail: fmt.Sprintf("unreachable (%v)", err),
			hint:   hint,
		}
	}
	return check{name: name, ok: true, detail: fmt.Sprintf("authenticated as %s", who)}
}

func checkDashboard(cfg *config.Config) check {
	if cfg.DashboardAddr == "" {
		return check{
			name:   "dashboard",
			ok:     true,
			detail: "disabled (set DASHBOARD_ADDR to enable)",
		}
	}
	if cfg.DashboardToken == "" {
		return check{
			name:   "dashboard",
			ok:     false,
			detail: "DASHBOARD_ADDR set but DASHBOARD_TOKEN is empty",
			hint:   "set DASHBOARD_TOKEN — Noctra refuses to start an unauthenticated dashboard",
		}
	}
	detail := "enabled at " + cfg.DashboardAddr
	if host, _, err := net.SplitHostPort(cfg.DashboardAddr); err == nil && (host == "0.0.0.0" || host == "::") {
		detail += " — exposed on all interfaces; only the token gates access"
	}
	return check{
		name:   "dashboard",
		ok:     true,
		detail: detail,
	}
}

func checkRepos(cfg *config.Config) check {
	if cfg.RepoPath != "" {
		return check{
			name:   "repos",
			ok:     true,
			detail: fmt.Sprintf("routed via Linear project `Repo:` directives; REPO_PATH fallback (%s)", cfg.RepoPath),
		}
	}
	return check{
		name:   "repos",
		ok:     true,
		detail: "routed via Linear project `Repo:` directives",
	}
}
