package agent

import (
	"context"
	"regexp"
)

// antigravityBackend runs Google's Antigravity CLI (`agy`) in non-interactive
// print mode. It authenticates via a one-time `agy` login on the host (Google
// AI Pro); Noctra does not manage those credentials.
type antigravityBackend struct{}

func (antigravityBackend) Name() string     { return "antigravity" }
func (antigravityBackend) Label() string    { return "Google Antigravity" }
func (antigravityBackend) CLI() string      { return "agy" }
func (antigravityBackend) CoAuthor() string { return "Antigravity <noreply@google.com>" }

func (b antigravityBackend) Run(ctx context.Context, opts RunOptions) error {
	return runCLI(ctx, b.CLI(), antigravityArgs(opts), nil, opts)
}

// antigravityArgs builds the argv for an agy run. Unlike Claude's boolean
// --print, agy's --print is a STRING flag whose value IS the prompt — so the
// auto-approve flag must precede it and the prompt must be the final token,
// else --print swallows the next flag and agy improvises.
func antigravityArgs(opts RunOptions) []string {
	return []string{
		"--dangerously-skip-permissions",
		"--print", opts.Prompt,
	}
}

var antigravityRateLimitRe = regexp.MustCompile(`(?i)rate.?limit|usage.?limit|quota|resource.?exhausted|exceeded.*limit|too many requests`)

func (antigravityBackend) HasRateLimit(output string) bool {
	return antigravityRateLimitRe.MatchString(output)
}
