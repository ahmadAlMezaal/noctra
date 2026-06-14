package agent

import (
	"context"
	"os"
	"regexp"
)

// claudeBackend runs Anthropic's Claude Code CLI (`claude`) in non-interactive
// print mode. This is Noctra's default and original backend.
type claudeBackend struct{}

func (claudeBackend) Name() string     { return "claude" }
func (claudeBackend) Label() string    { return "Claude Code" }
func (claudeBackend) CLI() string      { return "claude" }
func (claudeBackend) CoAuthor() string { return "Claude <noreply@anthropic.com>" }

// Run invokes `claude --print` in opts.Workdir. When UseAgentTeams is set the
// experimental agent-teams flag is exported into the child environment.
func (b claudeBackend) Run(ctx context.Context, opts RunOptions) error {
	var env []string
	if opts.UseAgentTeams {
		env = append(os.Environ(), "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
	}
	return runCLI(ctx, b.CLI(), claudeArgs(opts), env, opts)
}

// claudeArgs builds the argv for a Claude Code run. Split out from Run so the
// flag set is unit-testable without executing the CLI.
func claudeArgs(opts RunOptions) []string {
	return []string{
		"--dangerously-skip-permissions",
		"--print",
		"--output-format", "text",
		"-p", opts.Prompt,
	}
}

// claudeRateLimitRe matches the usage / rate-limit markers Claude Code emits.
var claudeRateLimitRe = regexp.MustCompile(`(?i)rate.limit|usage.limit|exceeded.*limit|too many requests`)

func (claudeBackend) HasRateLimit(output string) bool {
	return claudeRateLimitRe.MatchString(output)
}
