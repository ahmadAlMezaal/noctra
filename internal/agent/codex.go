package agent

import (
	"context"
	"regexp"
)

// codexBackend runs OpenAI's Codex CLI (`codex`) in its non-interactive
// automation mode (`codex exec`). It authenticates via a one-time `codex login`
// on the host (ChatGPT subscription or an OPENAI_API_KEY in the environment);
// Noctra does not manage those credentials.
//
// NOTE: the exact `codex exec` flags are pinned to the documented CLI surface
// at the time of writing — Codex isn't introspectable from this environment.
// If a future Codex release renames flags, only codexArgs needs to change.
type codexBackend struct{}

func (codexBackend) Name() string  { return "codex" }
func (codexBackend) Label() string { return "OpenAI Codex" }
func (codexBackend) CLI() string   { return "codex" }

// Run invokes `codex exec` in opts.Workdir. UseAgentTeams is Claude-only and
// is ignored here.
func (b codexBackend) Run(ctx context.Context, opts RunOptions) error {
	// nil env → inherit os.Environ (so OPENAI_API_KEY / login state flow through).
	return runCLI(ctx, b.CLI(), codexArgs(opts), nil, opts)
}

// codexArgs builds the argv for a Codex run. `exec` is Codex's non-interactive
// subcommand; the bypass flag is the autonomous-run analogue of Claude's
// --dangerously-skip-permissions (no approval prompts, full filesystem access)
// since Noctra runs unattended in a throwaway worktree. The prompt is
// passed as the positional argument. Split out from Run so the flag set is
// unit-testable without executing the CLI.
func codexArgs(opts RunOptions) []string {
	return []string{
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		opts.Prompt,
	}
}

// codexRateLimitRe matches the usage / rate-limit / quota markers Codex and the
// OpenAI backend emit. Superset of the Claude phrasings plus "quota" and the
// underscored API error code (rate_limit_exceeded).
var codexRateLimitRe = regexp.MustCompile(`(?i)rate.?limit|usage.?limit|quota|exceeded.*limit|too many requests`)

func (codexBackend) HasRateLimit(output string) bool {
	return codexRateLimitRe.MatchString(output)
}
