// Command nightshift is the autonomous Linear→PR agent.
//
// Subcommands:
//
//	nightshift            start the poll loop (default)
//	nightshift setup      interactive .env + repos.json wizard
//	nightshift cleanup    clean up stale branches and worktrees
//	nightshift cleanup --force
//	nightshift version
package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ahmadAlMezaal/nightshift/internal/cleanup"
	"github.com/ahmadAlMezaal/nightshift/internal/config"
	"github.com/ahmadAlMezaal/nightshift/internal/pipeline"
	"github.com/ahmadAlMezaal/nightshift/internal/setup"
)

// version is the build version. Defaults to a dev marker for `go build`/`go
// run`; release builds stamp the real tag via -ldflags "-X main.version=...".
var version = "2.0.0-dev"

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
		return runPoll(scriptDir)
	case "setup":
		return runSetup(scriptDir)
	case "cleanup":
		force := len(os.Args) > 2 && os.Args[2] == "--force"
		return runCleanup(scriptDir, force)
	case "version", "--version", "-v":
		fmt.Println("nightshift", version)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (try: run, setup, cleanup, version)", cmd)
	}
}

// resolveScriptDir picks the directory Nightshift treats as its "home" —
// where .env, repos.json, and .agent-logs live. We prefer the current working
// directory when it looks like a Nightshift checkout (for `go run`), and
// otherwise fall back to the directory next to the binary.
func resolveScriptDir() (string, error) {
	if cwd, err := os.Getwd(); err == nil {
		for _, marker := range []string{".env", "repos.json", ".env.example", "go.mod"} {
			if _, err := os.Stat(filepath.Join(cwd, marker)); err == nil {
				return cwd, nil
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func runPoll(scriptDir string) error {
	cfg, err := ensureValidConfig(scriptDir)
	if err != nil {
		return err
	}
	if err := requireCLIs(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("nightshift starting", "version", version)
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
	if err := requireCLIs(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cleanup.Run(ctx, cfg, force)
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
// (git/gh/claude) aren't on PATH. Surfaces all missing ones at once so the
// user can install them in a single round.
func requireCLIs() error {
	missing := config.CheckCLIs()
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
