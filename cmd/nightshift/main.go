// Command nightshift is the autonomous Linear→PR agent.
//
// Subcommands:
//
//	nightshift            start the poll loop (default)
//	nightshift setup      interactive .env + repos.json wizard
//	nightshift cleanup    clean up stale branches and worktrees
//	nightshift cleanup --force
//	nightshift doctor     preflight dependency and config checks
//	nightshift update     self-update to the latest release (--restart)
//	nightshift logs       tail the service logs (-f to follow)
//	nightshift version
//	nightshift --help
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

	"github.com/ahmadAlMezaal/nightshift/internal/cleanup"
	"github.com/ahmadAlMezaal/nightshift/internal/config"
	"github.com/ahmadAlMezaal/nightshift/internal/doctor"
	"github.com/ahmadAlMezaal/nightshift/internal/pipeline"
	"github.com/ahmadAlMezaal/nightshift/internal/selfupdate"
	"github.com/ahmadAlMezaal/nightshift/internal/setup"
)

// version is the build version. Defaults to a dev marker for `go build`/`go
// run`; release builds stamp the real tag via -ldflags "-X main.version=...".
var version = "2.0.0-dev"

// ANSI escape codes for the startup banner.
const (
	ansiAmber = "\033[1;33m"
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

// bannerArt is the figlet "standard" font rendering of "Nightshift".
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
	case "version", "--version", "-v":
		printBanner()
		fmt.Println("nightshift", version)
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

// printBanner prints a styled ASCII "Nightshift" banner with a moon emoji,
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
	fmt.Println("Usage: nightshift [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run       Start the poll loop (default)")
	fmt.Println("  setup     Interactive configuration wizard")
	fmt.Println("  cleanup   Clean up stale branches and worktrees")
	fmt.Println("  doctor    Preflight dependency and config checks")
	fmt.Println("  update    Self-update to the latest release (--restart to restart the service)")
	fmt.Println("  logs      Tail the service logs (-f / --follow to stream)")
	fmt.Println("  version   Print version information")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --help, -h   Show this help message")
	fmt.Println()
	fmt.Printf("Config dir: %s (override by running from a checkout with .env)\n", config.DefaultConfigDir())
}

// resolveScriptDir picks the directory Nightshift treats as its "home" —
// where .env, repos.json, and logs/ live. We prefer the current working
// directory when it looks like a Nightshift checkout (for `go run`), and
// otherwise fall back to the per-user config dir (~/.nightshift/).
func resolveScriptDir() (string, error) {
	if cwd, err := os.Getwd(); err == nil {
		for _, marker := range []string{".env", "repos.json", ".env.example", "go.mod"} {
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

	slog.Info("nightshift starting", "version", version)
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

// runLogs tails Nightshift's own service logs. On a systemd host it execs
// journalctl for the user unit (optionally following). When journalctl isn't
// available (macOS dev box, Docker), it points the user at where logs actually
// live instead of failing cryptically.
func runLogs(scriptDir string, follow bool) error {
	jctl, err := exec.LookPath("journalctl")
	if err != nil {
		fmt.Println("journalctl not found — Nightshift isn't running under systemd here.")
		fmt.Println()
		fmt.Printf("Per-ticket agent logs:  %s\n", filepath.Join(scriptDir, "logs"))
		fmt.Println("Running in Docker?       use `docker logs <container>`")
		return nil
	}

	args := []string{"--user-unit=nightshift.service"}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.Command(jctl, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
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
	fmt.Println("🌙 Welcome to Nightshift!")
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

// requireCLIs fails fast if any of the external commands Nightshift needs
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
