package agent

import (
	"context"
	"regexp"
)

// antigravityBackend runs Google's Antigravity CLI (`agy`) in print mode; auth is a one-time host `agy` login (Google AI Pro).
type antigravityBackend struct{}

func (antigravityBackend) Name() string     { return "antigravity" }
func (antigravityBackend) Label() string    { return "Google Antigravity" }
func (antigravityBackend) CLI() string      { return "agy" }
func (antigravityBackend) CoAuthor() string { return "Antigravity <noreply@google.com>" }

func (b antigravityBackend) Run(ctx context.Context, opts RunOptions) (Usage, error) {
	out, err := runCLI(ctx, b.CLI(), antigravityArgs(opts), nil, opts)
	return ParseUsage(out), err
}

// antigravityArgs builds the argv for an agy run. agy's --print is a STRING flag whose value IS the prompt, so the auto-approve flag must precede it and the prompt be the final token, else --print swallows the next flag.
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
