package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type copilotBackend struct{}

func (copilotBackend) Name() string  { return "copilot" }
func (copilotBackend) Label() string { return "GitHub Copilot" }
func (copilotBackend) CLI() string   { return "copilot" }
func (copilotBackend) CoAuthor() string {
	return "Copilot <223556219+Copilot@users.noreply.github.com>"
}

func (b copilotBackend) Run(ctx context.Context, opts RunOptions) (Usage, error) {
	out, err := runCLI(ctx, b.CLI(), copilotArgs(opts), copilotEnv(ctx), opts)
	return ParseUsage(out), err
}

func copilotEnv(ctx context.Context) []string {
	if os.Getenv("COPILOT_GITHUB_TOKEN") != "" {
		return nil
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
		if err != nil {
			return nil
		}
		token = strings.TrimSpace(string(out))
	}
	if token == "" {
		return nil
	}
	if strings.HasPrefix(token, "ghp_") {
		slog.Warn("copilot: gh is authenticated with a classic PAT, which Copilot does not accept; " +
			"not injecting GH_TOKEN. Re-auth gh with the OAuth web flow (`gh auth login`), use a " +
			"fine-grained PAT, or run `copilot /login`.")
		return nil
	}
	return append(os.Environ(), "COPILOT_GITHUB_TOKEN="+token)
}

func copilotArgs(opts RunOptions) []string {
	return []string{
		"--allow-all-tools",
		"--no-ask-user",
		"-p", opts.Prompt,
	}
}

var copilotRateLimitRe = regexp.MustCompile(`(?i)rate.?limit|usage.?limit|quota|exceeded.*limit|too many requests`)

func (copilotBackend) HasRateLimit(output string) bool {
	return copilotRateLimitRe.MatchString(output)
}
