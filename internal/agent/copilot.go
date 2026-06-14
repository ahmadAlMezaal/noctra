package agent

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// copilotBackend runs GitHub's Copilot CLI (`copilot`) in its non-interactive
// headless mode (`copilot --allow-all-tools --no-ask-user -p <prompt>`). It authenticates via
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

// Run invokes `copilot --allow-all-tools --no-ask-user -p <prompt>` in opts.Workdir.
// UseAgentTeams is Claude-only and is ignored here.
func (b copilotBackend) Run(ctx context.Context, opts RunOptions) error {
	return runCLI(ctx, b.CLI(), copilotArgs(opts), copilotEnv(ctx), opts)
}

// copilotEnv bridges a GitHub token into the Copilot CLI's environment. Copilot
// authenticates from COPILOT_GITHUB_TOKEN / GH_TOKEN / GITHUB_TOKEN, or an
// interactive `copilot /login`. In a headless systemd service none of those env
// vars are set and — unlike an interactive shell — copilot does NOT fall back to
// reading gh's credential store, so it dies immediately with "No authentication
// information found." even on a host where `gh` itself is authenticated (PRs
// still work because gh reads its own store). When no token env is present we
// mint one from `gh auth token` and inject GH_TOKEN, so copilot works wherever
// gh does — the same auth Noctra already relies on for PR creation. Returns nil
// (inherit os.Environ unchanged) when a token is already set or gh can't supply
// one, leaving copilot to surface its own auth guidance.
func copilotEnv(ctx context.Context) []string {
	for _, k := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if os.Getenv(k) != "" {
			return nil // already authenticated via env
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
	return append(os.Environ(), "GH_TOKEN="+token)
}

// copilotArgs builds the argv for a Copilot CLI run. `-p` is the one-shot
// non-interactive prompt flag; `--allow-all-tools` auto-approves file edits and
// commands (the Copilot analogue of Claude's --dangerously-skip-permissions /
// Codex's --dangerously-bypass-approvals-and-sandbox) since Noctra runs
// unattended in a throwaway worktree. `--no-ask-user` disables the `ask_user`
// tool: --allow-all-tools auto-approves tool *execution* but does NOT stop
// Copilot from pausing to ask a clarifying question, which on a headless run
// (no stdin) would hang until AGENT_TIMEOUT instead of returning so our
// BLOCKED: retry path can act. Split out from Run so the flag set is
// unit-testable without executing the CLI.
func copilotArgs(opts RunOptions) []string {
	return []string{
		"--allow-all-tools",
		"--no-ask-user",
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
