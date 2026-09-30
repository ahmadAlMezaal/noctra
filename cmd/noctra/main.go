package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/onelastcommit/noctra/internal/cleanup"
	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/configcmd"
	"github.com/onelastcommit/noctra/internal/doctor"
	"github.com/onelastcommit/noctra/internal/pipeline"
	"github.com/onelastcommit/noctra/internal/reposcmd"
	"github.com/onelastcommit/noctra/internal/selfupdate"
	"github.com/onelastcommit/noctra/internal/service"
	"github.com/onelastcommit/noctra/internal/setup"
	"github.com/onelastcommit/noctra/internal/sweepcmd"
)

var version = "0.4.0-dev"

const (
	ansiAmber = "\033[1;33m"
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

var bannerArt = "" +
	" _   _            _             \n" +
	"| \\ | | ___   ___| |_ _ __ __ _ \n" +
	"|  \\| |/ _ \\ / __| __| '__/ _` |\n" +
	"| |\\  | (_) | (__| |_| | | (_| |\n" +
	"|_| \\_|\\___/ \\___|\\__|_|  \\__,_|\n"

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintln(os.Stderr, "❌", err)
		os.Exit(1)
	}
}

func realMain() error {
	scriptDir, err := resolveScriptDir()
	if err != nil {
		return err
	}

	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "", "run":
		printBanner()
		return runPoll(scriptDir)
	case "setup":
		return runSetup(scriptDir)
	case "dashboard":
		return runDashboard(scriptDir, os.Args[2:])
	case "config":
		return configcmd.Run(scriptDir, os.Args[2:])
	case "repos":
		return reposcmd.Run(scriptDir, os.Args[2:])
	case "sweep":
		return sweepcmd.Run(scriptDir, os.Args[2:])
	case "cleanup":
		force := len(os.Args) > 2 && os.Args[2] == "--force"
		return runCleanup(scriptDir, force)
	case "doctor":
		jsonOut := false
		for _, a := range os.Args[2:] {
			if a == "--json" {
				jsonOut = true
			}
		}
		if jsonOut {
			return doctor.RunJSON(scriptDir, os.Stdout)
		}
		printBanner()
		return doctor.Run(scriptDir)
	case "update", "--update":
		restart := false
		for _, a := range os.Args[2:] {
			if a == "--restart" {
				restart = true
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return selfupdate.Update(ctx, version, restart)
	case "logs":
		follow := false
		for _, a := range os.Args[2:] {
			if a == "-f" || a == "--follow" {
				follow = true
			}
		}
		return runLogs(scriptDir, follow)
	case "tail":
		return runLogs(scriptDir, true)
	case "install-service":
		force, start := false, false
		for _, a := range os.Args[2:] {
			switch a {
			case "--force":
				force = true
			case "--start":
				start = true
			}
		}
		return service.Install(force, start)
	case "uninstall":
		purge, force, help, err := parseUninstallArgs(os.Args[2:])
		if err != nil {
			fmt.Fprint(os.Stderr, uninstallUsage)
			return err
		}
		if help {
			fmt.Print(uninstallUsage)
			return nil
		}
		if purge && !force {
			if !isInteractive() {
				return fmt.Errorf("refusing to --purge non-interactively without --force")
			}
			fmt.Println("⚠️  --purge permanently deletes Noctra's config, cloned repos, worktrees, and PR cursor:")
			if paths, err := service.PurgePaths(); err == nil {
				for _, p := range paths {
					fmt.Println("     " + p)
				}
			}
			if !askYesNo("Delete this state?", false) {
				return fmt.Errorf("uninstall --purge aborted")
			}
		}
		return service.Uninstall(purge)
	case "start", "stop", "restart", "status":
		return runService(cmd)
	case "completion":
		shell := ""
		if len(os.Args) > 2 {
			shell = os.Args[2]
		}
		script, err := completionScript(shell)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Usage: noctra completion bash|zsh")
			return err
		}
		fmt.Print(script)
		return nil
	case "version", "--version", "-v":
		printBanner()
		fmt.Println("noctra", version)
		printUpdateNotice()
		return nil
	case "help", "--help", "-h":
		printBanner()
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

func printBanner() {
	if !isCharDevice(os.Stdout) {
		return
	}
	fmt.Print(ansiAmber, bannerArt, ansiReset)
	fmt.Printf("  %s🌙 v%s%s — Autonomous Linear → PR agent\n\n", ansiDim, version, ansiReset)
}

func printUpdateNotice() {
	if version == "" || strings.Contains(version, "dev") || strings.Contains(version, "snapshot") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	latest, err := selfupdate.Latest(ctx)
	if err != nil || !selfupdate.IsNewer(latest, version) {
		return
	}
	fmt.Printf("\n🆙 A newer version is available: %s (run `noctra update` to upgrade)\n", latest)
}

func parseUninstallArgs(args []string) (purge, force, help bool, err error) {
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "--force", "-y":
			force = true
		case "--help", "-h":
			help = true
		default:
			return false, false, false, fmt.Errorf("unknown flag %q for uninstall", a)
		}
	}
	return purge, force, help, nil
}

const uninstallUsage = `Usage: noctra uninstall [--purge] [--force|-y]

Remove the systemd --user service and the installed binary. State is kept
unless --purge is given.

  --purge       also delete ~/.noctra* state (config + logs, cloned repos,
                worktrees, and the PR cursor)
  --force, -y   skip the --purge confirmation prompt (required for --purge
                when running non-interactively)
  --help, -h    show this message
`

func printUsage() {
	fmt.Println("Usage: noctra [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run       Start the poll loop (default)")
	fmt.Println("  setup     Interactive configuration wizard")
	fmt.Println("  dashboard SSH-tunnel to a remote dashboard and open it in your browser")
	fmt.Println("  config    Read or edit .env settings (path, edit, get, set)")
	fmt.Println("  repos     Add a repository or list the cloned ones (add, list)")
	fmt.Println("  sweep     Run a maintenance sweep now (--task, --repo, --force)")
	fmt.Println("  cleanup   Clean up stale branches and worktrees")
	fmt.Println("  doctor    Preflight dependency and config checks")
	fmt.Println("  update    Self-update to the latest release (--restart to restart the service)")
	fmt.Println("  install-service  Install the systemd --user unit (--start to enable+start, --force to overwrite)")
	fmt.Println("  uninstall  Remove the service + binary (--purge also deletes ~/.noctra* state, --force to skip the prompt)")
	fmt.Println("  logs      Tail the service logs (-f / --follow to stream)")
	fmt.Println("  tail      Stream the service logs live (alias for `logs -f`)")
	fmt.Println("  start     Start the systemd --user service")
	fmt.Println("  stop      Stop the systemd --user service")
	fmt.Println("  restart   Restart the systemd --user service")
	fmt.Println("  status    Show service status and installed version")
	fmt.Println("  completion  Print a shell-completion script (bash|zsh)")
	fmt.Println("  version   Print version information")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --help, -h   Show this help message")
	fmt.Println()
	fmt.Printf("Config dir: %s (override by running from a checkout with .env)\n", config.DefaultConfigDir())
}

func resolveScriptDir() (string, error) {
	if cwd, err := os.Getwd(); err == nil {
		for _, marker := range []string{".env", ".env.example", "go.mod"} {
			if _, err := os.Stat(filepath.Join(cwd, marker)); err == nil {
				return cwd, nil
			}
		}
	}
	return config.DefaultConfigDir(), nil
}

func runPoll(scriptDir string) error {
	cfg, err := ensureValidConfig(scriptDir)
	if err != nil {
		return err
	}
	if err := requireCLIs(cfg); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("noctra starting", "version", version)
	pipeline.Version = version
	return pipeline.New(cfg).Run(ctx)
}

func runSetup(scriptDir string) error {
	return setup.Run(scriptDir)
}

func runCleanup(scriptDir string, force bool) error {
	cfg, err := ensureValidConfig(scriptDir)
	if err != nil {
		return err
	}
	if err := requireCLIs(cfg); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cleanup.Run(ctx, cfg, force)
}

func logsArgs(follow bool) []string {
	args := []string{"--user-unit=noctra.service"}
	if follow {
		args = append(args, "-f")
	} else {
		args = append(args, "--no-pager", "-n", "200")
	}
	return args
}

func runLogs(scriptDir string, follow bool) error {
	jctl, err := exec.LookPath("journalctl")
	if err != nil {
		fmt.Println("journalctl not found — Noctra isn't running under systemd here.")
		fmt.Println()
		fmt.Printf("Per-ticket agent logs:  %s\n", filepath.Join(scriptDir, "logs"))
		fmt.Println("Running in Docker?       use `docker logs <container>`")
		return nil
	}

	cmd := exec.Command(jctl, logsArgs(follow)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runService(verb string) error {
	sctl, err := exec.LookPath("systemctl")
	if err != nil {
		fmt.Println("systemctl not found — Noctra isn't running under systemd here.")
		fmt.Println()
		fmt.Println("Running in Docker?  use `docker start/stop/restart <container>`.")
		fmt.Println("On macOS / a dev box, run `noctra run` directly instead.")
		if verb == "status" {
			fmt.Println("noctra", version)
		}
		return nil
	}

	cmd := exec.Command(sctl, "--user", verb, "noctra.service")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	runErr := cmd.Run()
	if verb == "status" {
		fmt.Println("noctra", version)
		return nil
	}
	if unitNotFound(runErr) {
		return errors.New(serviceNotInstalledHint)
	}
	return runErr
}

const serviceNotInstalledHint = "noctra.service is not installed — run `noctra install-service --start` first (`noctra setup` writes the config only)"

func unitNotFound(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 5
}

var subcommands = []string{
	"run", "setup", "dashboard", "config", "repos", "sweep", "update", "install-service", "uninstall", "logs", "tail", "start", "stop", "restart",
	"status", "doctor", "cleanup", "completion", "version", "help",
}

func completionScript(shell string) (string, error) {
	cmds := strings.Join(subcommands, " ")
	switch shell {
	case "bash":
		return "# bash completion for noctra\n" +
			"_noctra() {\n" +
			"    local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n" +
			"    if [ \"$COMP_CWORD\" -eq 1 ]; then\n" +
			"        COMPREPLY=( $(compgen -W \"" + cmds + "\" -- \"$cur\") )\n" +
			"    fi\n" +
			"}\n" +
			"complete -F _noctra noctra\n", nil
	case "zsh":
		return "#compdef noctra\n" +
			"# zsh completion for noctra\n" +
			"_noctra() {\n" +
			"    local -a cmds\n" +
			"    cmds=(" + cmds + ")\n" +
			"    _describe 'command' cmds\n" +
			"}\n" +
			"compdef _noctra noctra\n", nil
	default:
		return "", fmt.Errorf("unsupported shell %q (want bash or zsh)", shell)
	}
}

func ensureValidConfig(scriptDir string) (*config.Config, error) {
	cfg, err := config.Load(scriptDir)
	if err != nil {
		return nil, err
	}
	verr := cfg.Validate()
	if verr == nil {
		return cfg, nil
	}

	if !isInteractive() {
		return nil, verr
	}

	fmt.Println()
	fmt.Println("🌙 Welcome to Noctra!")
	fmt.Println()
	fmt.Println("Your configuration has gaps:")
	for _, line := range strings.Split(verr.Error(), "\n") {
		fmt.Println("  " + strings.TrimPrefix(line, "  "))
	}
	fmt.Println()
	if !askYesNo("Launch the setup wizard now?", true) {
		return nil, verr
	}

	if err := setup.Run(scriptDir); err != nil {
		return nil, err
	}
	cfg, err = config.Load(scriptDir)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config still invalid after setup: %w", err)
	}
	return cfg, nil
}

func requireCLIs(cfg *config.Config) error {
	missing := cfg.CheckCLIs()
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("required command(s) not found in PATH: %s — install before running",
		strings.Join(missing, ", "))
}

func isInteractive() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func askYesNo(prompt string, defaultYes bool) bool {
	suffix := " [y/N] "
	if defaultYes {
		suffix = " [Y/n] "
	}
	fmt.Print(prompt + suffix)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return defaultYes
	}
	s := strings.ToLower(strings.TrimSpace(sc.Text()))
	if s == "" {
		return defaultYes
	}
	return s == "y" || s == "yes"
}
