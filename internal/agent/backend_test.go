package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNew_SelectsBackend(t *testing.T) {
	cases := map[string]string{
		"":              "claude", // default
		"claude":        "claude",
		"Claude":        "claude", // case-insensitive
		" codex ":       "codex",  // trimmed
		"codex":         "codex",
		"copilot":       "copilot",
		" Copilot ":     "copilot", // case-insensitive + trimmed
		"antigravity":   "antigravity",
		" Antigravity ": "antigravity", // case-insensitive + trimmed
	}
	for in, wantName := range cases {
		b, err := New(in)
		if err != nil {
			t.Fatalf("New(%q) error: %v", in, err)
		}
		if b.Name() != wantName {
			t.Errorf("New(%q).Name() = %q, want %q", in, b.Name(), wantName)
		}
	}

	if _, err := New("gemini"); err == nil {
		t.Error("New(\"gemini\") should error on unknown backend")
	}
}

func TestBackend_CLIAndLabel(t *testing.T) {
	claude, err := New("claude")
	if err != nil {
		t.Fatalf("New(\"claude\") error: %v", err)
	}
	if claude.CLI() != "claude" {
		t.Errorf("claude CLI = %q, want claude", claude.CLI())
	}
	if claude.Label() != "Claude Code" {
		t.Errorf("claude Label = %q, want \"Claude Code\"", claude.Label())
	}
	codex, err := New("codex")
	if err != nil {
		t.Fatalf("New(\"codex\") error: %v", err)
	}
	if codex.CLI() != "codex" {
		t.Errorf("codex CLI = %q, want codex", codex.CLI())
	}
	if codex.Label() != "OpenAI Codex" {
		t.Errorf("codex Label = %q, want \"OpenAI Codex\"", codex.Label())
	}
	copilot, err := New("copilot")
	if err != nil {
		t.Fatalf("New(\"copilot\") error: %v", err)
	}
	if copilot.CLI() != "copilot" {
		t.Errorf("copilot CLI = %q, want copilot", copilot.CLI())
	}
	if copilot.Label() != "GitHub Copilot" {
		t.Errorf("copilot Label = %q, want \"GitHub Copilot\"", copilot.Label())
	}
	antigravity, err := New("antigravity")
	if err != nil {
		t.Fatalf("New(\"antigravity\") error: %v", err)
	}
	if antigravity.CLI() != "agy" {
		t.Errorf("antigravity CLI = %q, want agy", antigravity.CLI())
	}
	if antigravity.Label() != "Google Antigravity" {
		t.Errorf("antigravity Label = %q, want \"Google Antigravity\"", antigravity.Label())
	}
}

func TestClaudeArgs_PassesPromptInPrintMode(t *testing.T) {
	args := claudeArgs(RunOptions{Prompt: "do the thing"})
	if !slices.Contains(args, "--print") {
		t.Errorf("claudeArgs missing --print: %v", args)
	}
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("claudeArgs missing permission bypass: %v", args)
	}
	// Prompt is passed via -p <prompt>.
	i := slices.Index(args, "-p")
	if i < 0 || i+1 >= len(args) || args[i+1] != "do the thing" {
		t.Errorf("claudeArgs did not pass prompt after -p: %v", args)
	}
}

func TestCodexArgs_UsesExecAndPositionalPrompt(t *testing.T) {
	args := codexArgs(RunOptions{Prompt: "do the thing"})
	if len(args) == 0 || args[0] != "exec" {
		t.Errorf("codexArgs should start with exec subcommand: %v", args)
	}
	if !slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("codexArgs missing approval/sandbox bypass: %v", args)
	}
	// Prompt is the final positional argument.
	if args[len(args)-1] != "do the thing" {
		t.Errorf("codexArgs should end with the prompt: %v", args)
	}
}

func TestCopilotArgs_UsesAllowAllToolsAndPromptFlag(t *testing.T) {
	args := copilotArgs(RunOptions{Prompt: "do the thing"})
	if !slices.Contains(args, "--allow-all-tools") {
		t.Errorf("copilotArgs missing --allow-all-tools: %v", args)
	}
	// --no-ask-user keeps a headless run from hanging on a clarifying question.
	if !slices.Contains(args, "--no-ask-user") {
		t.Errorf("copilotArgs missing --no-ask-user: %v", args)
	}
	// Prompt is passed via -p <prompt>.
	i := slices.Index(args, "-p")
	if i < 0 || i+1 >= len(args) || args[i+1] != "do the thing" {
		t.Errorf("copilotArgs did not pass prompt after -p: %v", args)
	}
}

func TestAntigravityArgs_PromptIsValueOfPrintFlag(t *testing.T) {
	args := antigravityArgs(RunOptions{Prompt: "do the thing"})
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("antigravityArgs missing --dangerously-skip-permissions: %v", args)
	}
	// --print is a string flag: skip-permissions must precede it, and the
	// prompt must be the final token (its value) — else --print eats the next flag.
	i := slices.Index(args, "--print")
	if i < 0 || i+1 >= len(args) || args[i+1] != "do the thing" || args[len(args)-1] != "do the thing" {
		t.Errorf("antigravityArgs did not pass prompt as the value of --print: %v", args)
	}
	if skip := slices.Index(args, "--dangerously-skip-permissions"); skip > i {
		t.Errorf("--dangerously-skip-permissions must come before --print: %v", args)
	}
}

func TestCopilotEnv_SkipsWhenTokenAlreadySet(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "already-here")
	if env := copilotEnv(context.Background()); env != nil {
		t.Errorf("expected nil (inherit os.Environ) when a token env is set, got %v", env)
	}
}

func TestCopilotEnv_InjectsGhTokenWhenNoneSet(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	// Fake `gh` on PATH that prints a token for `gh auth token`.
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = token ]; then echo faketoken123; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	env := copilotEnv(context.Background())
	if env == nil {
		t.Fatal("expected env with injected GH_TOKEN, got nil")
	}
	if !slices.Contains(env, "GH_TOKEN=faketoken123") {
		t.Errorf("expected GH_TOKEN=faketoken123 in env, got %v", env)
	}
}

func TestCopilotEnv_SkipsClassicPAT(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	// Fake `gh` that returns a classic PAT (ghp_), which Copilot rejects.
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = token ]; then echo ghp_classicclassicclassic; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if env := copilotEnv(context.Background()); env != nil {
		t.Errorf("expected nil (no injection) for a classic PAT, got %v", env)
	}
}

func TestBackend_CoAuthor(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"claude", "Claude <noreply@anthropic.com>"},
		{"codex", "Codex <noreply@openai.com>"},
		{"copilot", "Copilot <223556219+Copilot@users.noreply.github.com>"},
		{"antigravity", "Antigravity <noreply@google.com>"},
	}
	for _, tc := range cases {
		b, err := New(tc.name)
		if err != nil {
			t.Fatalf("New(%q) error: %v", tc.name, err)
		}
		if got := b.CoAuthor(); got != tc.want {
			t.Errorf("%s.CoAuthor() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHasRateLimit_PerBackend(t *testing.T) {
	claude, _ := New("claude")
	codex, _ := New("codex")
	copilot, _ := New("copilot")
	antigravity, _ := New("antigravity")

	// Shared phrasings all backends must catch.
	shared := map[string]bool{
		"All good":                                     false,
		"Error: rate limit exceeded":                   true,
		"Error: too many requests":                     true,
		"Hit a usage limit on the API":                 true,
		"You have exceeded the daily request limit":    true,
		"nothing wrong here, just chatting about apis": false,
	}
	for in, want := range shared {
		if got := claude.HasRateLimit(in); got != want {
			t.Errorf("claude.HasRateLimit(%q) = %v, want %v", in, got, want)
		}
		if got := codex.HasRateLimit(in); got != want {
			t.Errorf("codex.HasRateLimit(%q) = %v, want %v", in, got, want)
		}
		if got := copilot.HasRateLimit(in); got != want {
			t.Errorf("copilot.HasRateLimit(%q) = %v, want %v", in, got, want)
		}
		if got := antigravity.HasRateLimit(in); got != want {
			t.Errorf("antigravity.HasRateLimit(%q) = %v, want %v", in, got, want)
		}
	}

	// Codex / OpenAI-specific phrasings the claude regex doesn't need to catch.
	codexOnly := []string{
		"Error: rate_limit_exceeded",
		"You have exceeded your current quota",
	}
	for _, in := range codexOnly {
		if !codex.HasRateLimit(in) {
			t.Errorf("codex.HasRateLimit(%q) = false, want true", in)
		}
	}

	// Copilot / GitHub-specific phrasings.
	copilotOnly := []string{
		"Error: rate_limit_exceeded",
		"You have exceeded your current quota",
	}
	for _, in := range copilotOnly {
		if !copilot.HasRateLimit(in) {
			t.Errorf("copilot.HasRateLimit(%q) = false, want true", in)
		}
	}

	// Antigravity / Gemini-specific phrasings (Google API status codes).
	antigravityOnly := []string{
		"You have exceeded your current quota",
		"error: RESOURCE_EXHAUSTED",
	}
	for _, in := range antigravityOnly {
		if !antigravity.HasRateLimit(in) {
			t.Errorf("antigravity.HasRateLimit(%q) = false, want true", in)
		}
	}
}
