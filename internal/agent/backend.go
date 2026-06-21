package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrTimedOut is returned when the coding agent is killed because the
// per-attempt timeout fires.
var ErrTimedOut = errors.New("agent timed out")

// RunOptions configures one invocation of a coding-agent CLI.
type RunOptions struct {
	Workdir string
	Prompt  string
	LogFile string
	Timeout time.Duration
	// UseAgentTeams is Claude-specific (enables CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS).
	// Backends that don't support it ignore it.
	UseAgentTeams bool
}

// Backend abstracts the underlying coding-agent CLI Noctra shells out to.
// Four implementations exist — Claude Code (default), OpenAI Codex, GitHub
// Copilot, and Google Antigravity — selected by AGENT_BACKEND.
//
// Everything else in this package is backend-agnostic on purpose: the prompt
// builders ask the agent to print "BLOCKED: <reason>" so BlockedLine works
// regardless of CLI, and the log/offset/summary helpers don't care which
// binary produced the bytes. Only two things actually differ per backend —
// the CLI invocation (flags + how the prompt is passed) and the phrasing of
// usage/rate-limit errors (HasRateLimit).
type Backend interface {
	// Name is the canonical backend identifier ("claude" / "codex" / "copilot" / "antigravity").
	Name() string
	// Label is the human-friendly backend name for banners / logs
	// (e.g. "Claude Code", "OpenAI Codex").
	Label() string
	// CLI is the executable Noctra requires on PATH for this backend.
	CLI() string
	// CoAuthor returns the "Name <email>" value for a Co-authored-by git
	// trailer attributing commits to this backend. Backends with a real
	// GitHub account behind the email (e.g. Copilot) get an avatar and
	// Contributors-graph entry; others render as a plain name on the commit.
	// Returns "" if no trailer should be added.
	CoAuthor() string
	// Run invokes the CLI in opts.Workdir, streaming stdout+stderr to
	// opts.LogFile. It returns ErrTimedOut (wrapped) on per-attempt timeout
	// and the underlying error on any other non-zero exit.
	Run(ctx context.Context, opts RunOptions) error
	// HasRateLimit reports whether output contains this backend's usage /
	// rate-limit markers.
	HasRateLimit(output string) bool
}

// New returns the Backend selected by name. An empty name defaults to Claude,
// matching DefaultAgentBackend in internal/config. An unknown name is an
// error — config.Validate rejects it up front, so this is a defensive guard.
func New(name string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "claude":
		return claudeBackend{}, nil
	case "codex":
		return codexBackend{}, nil
	case "copilot":
		return copilotBackend{}, nil
	case "antigravity":
		return antigravityBackend{}, nil
	default:
		return nil, fmt.Errorf("unknown agent backend %q (want \"claude\", \"codex\", \"copilot\", or \"antigravity\")", name)
	}
}

// runCLI is the shared exec plumbing for every backend: it applies the
// per-attempt timeout, writes the DEBUG header so the log carries enough
// context to diagnose a run, streams stdout+stderr to the log file, and maps a
// deadline kill to ErrTimedOut. bin/args are the backend-specific command; env
// is the full process environment to run with (pass nil to inherit os.Environ).
func runCLI(ctx context.Context, bin string, args, env []string, opts RunOptions) error {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	logF, err := os.OpenFile(opts.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer logF.Close()

	branch, _ := currentBranch(ctx, opts.Workdir)
	fmt.Fprintf(logF, "DEBUG: pwd = %s\nDEBUG: branch = %s\n", opts.Workdir, branch)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.Workdir
	cmd.Stdout = logF
	cmd.Stderr = logF
	if env != nil {
		cmd.Env = env
	}

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%w: %w", ErrTimedOut, err)
		}
		return err
	}
	return nil
}

func currentBranch(ctx context.Context, workdir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(trimNL(out)), nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
