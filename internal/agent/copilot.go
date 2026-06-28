package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// copilotBackend runs GitHub's Copilot CLI headless (`copilot --allow-all-tools --no-ask-user -p`); auth is a Copilot subscription tied to gh (GH_TOKEN or prior `gh auth login`). Flags are pinned to the documented surface — if a release renames them, only copilotArgs changes.
type copilotBackend struct{}

func (copilotBackend) Name() string  { return "copilot" }
func (copilotBackend) Label() string { return "GitHub Copilot" }
func (copilotBackend) CLI() string   { return "copilot" }
func (copilotBackend) CoAuthor() string {
	return "Copilot <223556219+Copilot@users.noreply.github.com>"
}

// Run invokes copilot in opts.Workdir; UseAgentTeams is Claude-only and ignored.
func (b copilotBackend) Run(ctx context.Context, opts RunOptions) (Usage, error) {
	out, err := runCLI(ctx, b.CLI(), copilotArgs(opts), copilotEnv(ctx), opts)
	return ParseUsage(out), err
}

// copilotEnv bridges a GitHub token into Copilot's env. Headless, Copilot reads only COPILOT_GITHUB_TOKEN/GH_TOKEN/GITHUB_TOKEN (not gh's store), so when none is set we mint one via `gh auth token` and inject GH_TOKEN. Returns nil (inherit env) when a token is already set or gh can't supply one.
func copilotEnv(ctx context.Context) []string {
	for _, k := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if os.Getenv(k) != "" {
			return nil
		}
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return nil
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return nil
	}
	// Copilot rejects classic PATs (ghp_) and reads the env token before its own store, so injecting one would break a valid `copilot /login`. Only bridge tokens it accepts (gho_/github_pat_).
	if strings.HasPrefix(token, "ghp_") {
		slog.Warn("copilot: gh is authenticated with a classic PAT, which Copilot does not accept; " +
			"not injecting GH_TOKEN. Re-auth gh with the OAuth web flow (`gh auth login`), use a " +
			"fine-grained PAT, or run `copilot /login`.")
		return nil
	}
	return append(os.Environ(), "GH_TOKEN="+token)
}

// copilotArgs builds the argv for a Copilot run: -p is the one-shot prompt, --allow-all-tools auto-approves edits/commands (analogue of Claude's skip-permissions), and --no-ask-user disables ask_user so a headless run can't hang on a clarifying question instead of returning via BLOCKED. Split out so the flag set is unit-testable.
func copilotArgs(opts RunOptions) []string {
	return []string{
		"--allow-all-tools",
		"--no-ask-user",
		"-p", opts.Prompt,
	}
}

// copilotRateLimitRe matches Copilot/GitHub usage/rate-limit/quota markers — shared phrasings plus "quota" and rate_limit_exceeded.
var copilotRateLimitRe = regexp.MustCompile(`(?i)rate.?limit|usage.?limit|quota|exceeded.*limit|too many requests`)

func (copilotBackend) HasRateLimit(output string) bool {
	return copilotRateLimitRe.MatchString(output)
}
