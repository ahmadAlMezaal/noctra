package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ErrTimedOut is returned when Claude is killed because the per-attempt
// timeout fires.
var ErrTimedOut = errors.New("claude timed out")

// RunOptions configures one invocation of the Claude CLI.
type RunOptions struct {
	Workdir       string
	Prompt        string
	LogFile       string
	Timeout       time.Duration
	UseAgentTeams bool
}

// Run invokes `claude --print` in Workdir, streaming both stdout and stderr to
// LogFile. A DEBUG header (pwd + branch) is written before Claude starts so
// the log carries enough context to diagnose what happened.
//
// If the per-attempt timeout fires, Run returns ErrTimedOut wrapped with the
// exec.Error. Any other non-zero exit is returned as the underlying error.
func Run(ctx context.Context, opts RunOptions) error {
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

	cmd := exec.CommandContext(ctx, "claude",
		"--dangerously-skip-permissions",
		"--print",
		"--output-format", "text",
		"-p", opts.Prompt,
	)
	cmd.Dir = opts.Workdir
	cmd.Stdout = logF
	cmd.Stderr = logF
	if opts.UseAgentTeams {
		cmd.Env = append(os.Environ(), "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
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
