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
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
)

const version = "2.0.0-dev"

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
	cfg, err := config.Load(scriptDir)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("nightshift starting",
		"version", version,
		"team", cfg.LinearTeamKey,
		"trigger", cfg.TriggerState,
		"max_concurrent", cfg.MaxConcurrent,
	)

	// TODO(ENG-166): wire up internal/pipeline.Run(ctx, cfg)
	_ = ctx
	return fmt.Errorf("poll loop not yet implemented — Go port in progress")
}

func runSetup(scriptDir string) error {
	// TODO(ENG-166): wire up internal/setup.Run(scriptDir)
	_ = scriptDir
	return fmt.Errorf("setup wizard not yet implemented — Go port in progress")
}

func runCleanup(scriptDir string, force bool) error {
	// TODO(ENG-166): wire up internal/cleanup.Run(scriptDir, force)
	_ = scriptDir
	_ = force
	return fmt.Errorf("cleanup not yet implemented — Go port in progress")
}
