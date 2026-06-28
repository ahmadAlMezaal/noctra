package agent

import (
	"context"
	"regexp"
)

// codexBackend runs OpenAI's Codex CLI (`codex exec`); auth is a one-time host `codex login` or OPENAI_API_KEY. Flags are pinned to the documented surface — if a release renames them, only codexArgs changes.
type codexBackend struct{}

func (codexBackend) Name() string     { return "codex" }
func (codexBackend) Label() string    { return "OpenAI Codex" }
func (codexBackend) CLI() string      { return "codex" }
func (codexBackend) CoAuthor() string { return "Codex <noreply@openai.com>" }

// Run invokes `codex exec` in opts.Workdir; UseAgentTeams is Claude-only and ignored.
func (b codexBackend) Run(ctx context.Context, opts RunOptions) (Usage, error) {
	// nil env → inherit os.Environ (so OPENAI_API_KEY / login state flow through).
	out, err := runCLI(ctx, b.CLI(), codexArgs(opts), nil, opts)
	return ParseUsage(out), err
}

// codexArgs builds the argv for a Codex run: the bypass flag is the autonomous-run analogue of Claude's --dangerously-skip-permissions (Noctra runs unattended in a throwaway worktree); prompt is positional. Split out so the flag set is unit-testable.
func codexArgs(opts RunOptions) []string {
	return []string{
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		opts.Prompt,
	}
}

// codexRateLimitRe matches Codex/OpenAI usage/rate-limit/quota markers — Claude's phrasings plus "quota" and rate_limit_exceeded.
var codexRateLimitRe = regexp.MustCompile(`(?i)rate.?limit|usage.?limit|quota|exceeded.*limit|too many requests`)

func (codexBackend) HasRateLimit(output string) bool {
	return codexRateLimitRe.MatchString(output)
}
