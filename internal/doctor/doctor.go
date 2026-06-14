// Package doctor implements `noctra doctor` — a preflight check that
// validates dependencies, credentials, and config before you try to run.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
)

// check is a single pass/fail diagnostic result.
type check struct {
	name   string
	ok     bool
	detail string
	hint   string
}

// gather runs every preflight check and returns the results. It performs no
// output, so both the human (`Run`) and machine (`RunJSON`) renderers share it.
func gather(scriptDir string) []check {
	var checks []check

	// Load config first so the CLI check knows which agent backend (and thus
	// which agent CLI) is required. If config can't load, fall back to the
	// default backend's CLI set so we still surface useful diagnostics.
	cfg, loadErr := config.Load(scriptDir)

	// ── Required CLIs ────────────────────────────────────────────────────────
	var clis []string
	if loadErr == nil {
		clis = cfg.RequiredCLIs()
	} else {
		// Config didn't load — check git/gh plus the default backend's CLI.
		clis = []string{"git", "gh", config.AgentCLIs()[config.DefaultAgentBackend]}
	}
	for _, cli := range clis {
		checks = append(checks, checkCLI(cli))
	}

	// ── gh auth ──────────────────────────────────────────────────────────────
	checks = append(checks, checkGHAuth())

	// ── Config + Linear ──────────────────────────────────────────────────────
	if loadErr != nil {
		checks = append(checks, check{
			name:   "config",
			detail: loadErr.Error(),
			hint:   "Run `noctra setup` to generate config files.",
		})
	} else {
		checks = append(checks, checkLinearKey(cfg))
		checks = append(checks, checkRepos(cfg))
	}

	// ── Config dir ───────────────────────────────────────────────────────────
	checks = append(checks, check{
		name:   "config dir",
		ok:     true,
		detail: scriptDir,
	})

	return checks
}

// Run performs all preflight checks and prints a human-readable report.
func Run(scriptDir string) error {
	fmt.Println("Checking dependencies and configuration...")
	fmt.Println()

	checks := gather(scriptDir)

	// ── Report ───────────────────────────────────────────────────────────────
	passed, failed := 0, 0
	for _, c := range checks {
		if c.ok {
			passed++
			fmt.Printf("  ✓ %-16s %s\n", c.name, c.detail)
		} else {
			failed++
			fmt.Printf("  ✗ %-16s %s\n", c.name, c.detail)
			if c.hint != "" {
				fmt.Printf("    %-16s %s\n", "", c.hint)
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("  %d passed, %d failed\n", passed, failed)
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Printf("  All %d checks passed — ready to roll.\n", passed)
	return nil
}

// jsonCheck is the machine-readable shape of a single check, emitted by
// `noctra doctor --json`.
type jsonCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// RunJSON performs all preflight checks and writes them to w as a JSON array
// of {name, ok, detail, hint} objects. It returns a non-nil error when any
// check failed (matching Run's exit semantics) so `--json` callers can still
// branch on success, but the JSON is always written first.
func RunJSON(scriptDir string, w io.Writer) error {
	checks := gather(scriptDir)

	out := make([]jsonCheck, 0, len(checks))
	failed := 0
	for _, c := range checks {
		if !c.ok {
			failed++
		}
		out = append(out, jsonCheck{Name: c.name, OK: c.ok, Detail: c.detail, Hint: c.hint})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

// checkCLI verifies a single CLI binary is on PATH.
func checkCLI(name string) check {
	path, err := exec.LookPath(name)
	if err != nil {
		hints := map[string]string{
			"git":     "Install git: https://git-scm.com/downloads",
			"gh":      "Install GitHub CLI: https://cli.github.com",
			"claude":  "Install Claude Code: https://docs.anthropic.com/en/docs/claude-code",
			"codex":   "Install Codex CLI: npm i -g @openai/codex, then run `codex login`",
			"copilot": "Install Copilot CLI: npm i -g @github/copilot (authenticates via `gh auth login` / GH_TOKEN)",
		}
		return check{
			name:   name,
			detail: "not found in PATH",
			hint:   hints[name],
		}
	}
	return check{name: name, ok: true, detail: path}
}

// checkGHAuth verifies `gh` is authenticated.
func checkGHAuth() check {
	if _, err := exec.LookPath("gh"); err != nil {
		return check{
			name:   "gh auth",
			detail: "skipped (gh not installed)",
		}
	}

	out, err := exec.Command("gh", "auth", "status").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return check{
			name:   "gh auth",
			detail: detail,
			hint:   "Run `gh auth login` to authenticate.",
		}
	}

	// Extract the account line from gh auth status output.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Logged in") {
			return check{name: "gh auth", ok: true, detail: strings.TrimSpace(line)}
		}
		if strings.Contains(line, "account") {
			return check{name: "gh auth", ok: true, detail: strings.TrimSpace(line)}
		}
	}
	return check{name: "gh auth", ok: true, detail: "authenticated"}
}

// checkLinearKey tests whether the configured Linear credential can reach
// Linear. It prefers an app-actor OAuth token (LINEAR_OAUTH_TOKEN) when set,
// falling back to the personal API key.
func checkLinearKey(cfg *config.Config) check {
	name := "LINEAR_API_KEY"
	var client *linear.Client
	switch {
	case cfg.LinearOAuthToken != "":
		name = "LINEAR_OAUTH_TOKEN"
		client = linear.NewOAuth(cfg.LinearOAuthToken)
	case cfg.LinearAPIKey != "":
		client = linear.New(cfg.LinearAPIKey)
	default:
		return check{
			name:   name,
			detail: "not set",
			hint:   "Run `noctra setup` or set LINEAR_API_KEY (or LINEAR_OAUTH_TOKEN) in .env.",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	who, err := client.Ping(ctx)
	if err != nil {
		return check{
			name:   name,
			detail: fmt.Sprintf("unreachable (%v)", err),
			hint:   "Check your credential in .env or at https://linear.app/settings/api.",
		}
	}
	return check{name: name, ok: true, detail: fmt.Sprintf("authenticated as %s", who)}
}

// checkRepos reports how repos are routed. Repos are resolved per-ticket from
// each Linear project's `Repo: owner/name` directive, with REPO_PATH as an
// optional single-repo fallback — there's nothing to validate up front, so this
// is always an informational OK.
func checkRepos(cfg *config.Config) check {
	if cfg.RepoPath != "" {
		return check{
			name:   "repos",
			ok:     true,
			detail: fmt.Sprintf("routed via Linear project `Repo:` directives; REPO_PATH fallback (%s)", cfg.RepoPath),
		}
	}
	return check{
		name:   "repos",
		ok:     true,
		detail: "routed via Linear project `Repo:` directives",
	}
}
