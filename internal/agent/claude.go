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

// Run invokes `claude --print --output-format json`, unwrapping the JSON result
// into the log so the log's text consumers see the message, and returns the
// usage/cost from the envelope. Falls back to raw output (errors, rate-limit
// messages, text mode) when stdout isn't a JSON result object.
func (b claudeBackend) Run(ctx context.Context, opts RunOptions) (Usage, error) {
	var env []string
	if opts.UseAgentTeams {
		env = append(os.Environ(), "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
	}
	stdout, stderr, err := runCLICapture(ctx, b.CLI(), claudeArgs(opts), env, opts)

	if usage, result, ok := ParseClaudeJSON(stdout); ok {
		writeRunLog(ctx, opts, result+stderr)
		return usage, err
	}
	writeRunLog(ctx, opts, stdout+stderr)
	return ParseUsage(stdout + "\n" + stderr), err
}

// claudeArgs builds the argv for a Claude Code run. Split out from Run so the
// flag set is unit-testable without executing the CLI.
func claudeArgs(opts RunOptions) []string {
	return []string{
		"--dangerously-skip-permissions",
		"--print",
		"--output-format", "json",
		"-p", opts.Prompt,
	}
}

// claudeRateLimitRe matches the usage / rate-limit markers Claude Code emits.
var claudeRateLimitRe = regexp.MustCompile(`(?i)rate.limit|usage.limit|exceeded.*limit|too many requests`)

func (claudeBackend) HasRateLimit(output string) bool {
	return claudeRateLimitRe.MatchString(output)
}
