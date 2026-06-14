// Command noctra is the autonomous Linear→PR agent.
//
// Subcommands:
//
//	noctra            start the poll loop (default)
//	noctra setup      interactive .env wizard
//	noctra cleanup    clean up stale branches and worktrees
//	noctra cleanup --force
//	noctra doctor     preflight dependency and config checks
//	noctra update     self-update to the latest release (--restart)
//	noctra install-service  install the systemd --user unit (--start, --force)
//	noctra logs       tail the service logs (-f to follow)
//	noctra start      start the systemd --user service
//	noctra stop       stop the systemd --user service
//	noctra restart    restart the systemd --user service
//	noctra status     show service status + installed version
//	noctra completion print a bash/zsh completion script
//	noctra version
//	noctra --help
package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ahmadAlMezaal/noctra/internal/cleanup"
	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/doctor"
	"github.com/ahmadAlMezaal/noctra/internal/pipeline"
	"github.com/ahmadAlMezaal/noctra/internal/selfupdate"
	"github.com/ahmadAlMezaal/noctra/internal/service"
	"github.com/ahmadAlMezaal/noctra/internal/setup"
)

// version is the build version. Defaults to a dev marker for `go build`/`go
// run`; release builds stamp the real tag via -ldflags "-X main.version=...".
var version = "0.4.0-dev"

// ANSI escape codes for the startup banner.
const (
	ansiAmber = "\033[1;33m"
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

// bannerArt is the figlet "standard" font rendering of "Noctra".
// Defined as a regular string (not raw) because the font uses backticks.
var bannerArt = "" +
	"  _   _ _       _     _       _     _  __ _   \n" +
	" | \\ | (_) __ _| |__ | |_ ___| |__ (_)/ _| |_ \n" +
	" |  \\| | |/ _` | '_ \\| __/ __| '_ \\| | |_| __|\n" +
	" | |\\  | | (_| | | | | |_\\__ \\ | | | |  _| |_ \n" +
	" |_| \\_|_|\\__, |_| |_|\\__|___/_| |_|_|_|  \\__|\n" +
	"           |___/                                \n"

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

// printBanner prints a styled ASCII "Noctra" banner with a moon emoji,
// the version, and a tagline. TTY-aware: skipped when stdout is not a
// terminal (systemd, cron, piped) so service logs stay clean.
func printBanner() {
	if !isCharDevice(os.Stdout) {
		return
	}
	fmt.Print(ansiAmber, bannerArt, ansiReset)
	fmt.Printf("  %s🌙 v%s%s — Autonomous Linear → PR agent\n\n", ansiDim, version, ansiReset)
}

// printUsage prints the CLI usage/help screen.
func printUsage() {
	fmt.Println("Usage: noctra [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run       Start the poll loop (default)")
	fmt.Println("  setup     Interactive configuration wizard")
	fmt.Println("  cleanup   Clean up stale branches and worktrees")
	fmt.Println("  doctor    Preflight dependency and config checks")
	fmt.Println("  update    Self-update to the latest release (--restart to restart the service)")
	fmt.Println("  install-service  Install the systemd --user unit (--start to enable+start, --force to overwrite)")
	fmt.Println("  logs      Tail the service logs (-f / --follow to stream)")
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

// resolveScriptDir picks the directory Noctra treats as its "home" —
// where .env and logs/ live. We prefer the current working directory when it
// looks like a Noctra checkout (for `go run`), and otherwise fall back to
// the per-user config dir (~/.noctra/).
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

// logsArgs returns the journalctl arguments for `noctra logs`. Extracted
// as a pure function so the flag logic is unit-testable.
func logsArgs(follow bool) []string {
	args := []string{"--user-unit=noctra.service"}
	if follow {
		args = append(args, "-f")
	} else {
		args = append(args, "-n", "200")
	}
	return args
}

// runLogs tails Noctra's own service logs. On a systemd host it execs
// journalctl for the user unit (optionally following). When journalctl isn't
// available (macOS dev box, Docker), it points the user at where logs actually
// live instead of failing cryptically.
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

// runService is a thin wrapper over `systemctl --user <verb> noctra.service`
// for the start/stop/restart/status subcommands. stdout/stderr stream through.
// On a non-systemd host (macOS dev box, Docker) systemctl isn't on PATH, so we
// print a clear hint instead of crashing — mirroring runLogs. For `status` we
// also print the installed binary version after the systemctl output.
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
	// `systemctl status` exits non-zero for inactive/dead units; that's
	// informational, not an error we want to surface as a failed command.
	runErr := cmd.Run()
	if verb == "status" {
		fmt.Println("noctra", version)
		return nil
	}
	return runErr
}

// subcommands is the list completion offers. Kept in one place so the help
// text, the completion script, and tests stay in sync.
var subcommands = []string{
	"run", "setup", "update", "install-service", "logs", "start", "stop", "restart",
	"status", "doctor", "cleanup", "completion", "version", "help",
}

// completionScript returns a static shell-completion script for the given
// shell ("bash" or "zsh") completing the subcommand list. It's a pure function
// (no I/O) so it can be unit-tested. An unknown shell returns an error.
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

// ensureValidConfig loads + validates config. If validation fails and we're
// running interactively, it offers to launch the setup wizard inline so first-
// run users don't get a cryptic error and walk away. Non-interactive runs
// (systemd, cron) just get the validation error.
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
	// Reload after the wizard wrote the files
	cfg, err = config.Load(scriptDir)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config still invalid after setup: %w", err)
	}
	return cfg, nil
}

// requireCLIs fails fast if any of the external commands Noctra needs
// (git/gh + the selected agent backend's CLI) aren't on PATH. Surfaces all
// missing ones at once so the user can install them in a single round.
func requireCLIs(cfg *config.Config) error {
	missing := cfg.CheckCLIs()
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("required command(s) not found in PATH: %s — install before running",
		strings.Join(missing, ", "))
}

// isInteractive reports whether the user is likely sitting at a terminal.
// Used to decide whether it's safe to auto-launch the setup wizard inline.
//
// We require BOTH stdin and stdout to be character devices. Checking only
// stdin would treat `< /dev/null` (also a char device) as interactive; under
// systemd, stdout is a journald socket (not a char device), so requiring both
// correctly classifies that case as non-interactive even though stdin happens
// to be /dev/null.
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
