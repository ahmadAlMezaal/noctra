package agent

import (
	"context"
	"regexp"
)

// copilotBackend runs GitHub's Copilot CLI (`copilot`) in its non-interactive
// headless mode (`copilot -p <prompt> --allow-all-tools`). It authenticates via
// a Copilot subscription tied to `gh` (so `GH_TOKEN` or a prior `gh auth login`
// is enough); Noctra does not manage those credentials.
//
// NOTE: the exact flags are pinned to the documented CLI surface at the time of
// writing. If a future Copilot release renames flags, only copilotArgs needs to
// change.
type copilotBackend struct{}

func (copilotBackend) Name() string  { return "copilot" }
func (copilotBackend) Label() string { return "GitHub Copilot" }
func (copilotBackend) CLI() string   { return "copilot" }

// Run invokes `copilot -p <prompt> --allow-all-tools` in opts.Workdir.
// UseAgentTeams is Claude-only and is ignored here.
func (b copilotBackend) Run(ctx context.Context, opts RunOptions) error {
	// nil env → inherit os.Environ (so GH_TOKEN / gh auth state flow through).
	return runCLI(ctx, b.CLI(), copilotArgs(opts), nil, opts)
}

// copilotArgs builds the argv for a Copilot CLI run. `-p` is the one-shot
// non-interactive prompt flag; `--allow-all-tools` auto-approves file edits and
// commands (the Copilot analogue of Claude's --dangerously-skip-permissions /
// Codex's --dangerously-bypass-approvals-and-sandbox) since Noctra runs
// unattended in a throwaway worktree. Split out from Run so the flag set is
// unit-testable without executing the CLI.
func copilotArgs(opts RunOptions) []string {
	return []string{
		"--allow-all-tools",
		"-p", opts.Prompt,
	}
}

// copilotRateLimitRe matches the usage / rate-limit / quota markers the Copilot
// CLI and GitHub backend emit. Covers the shared phrasings plus GitHub-specific
// "quota" and the underscored API error code (rate_limit_exceeded).
var copilotRateLimitRe = regexp.MustCompile(`(?i)rate.?limit|usage.?limit|quota|exceeded.*limit|too many requests`)

func (copilotBackend) HasRateLimit(output string) bool {
	return copilotRateLimitRe.MatchString(output)
}
