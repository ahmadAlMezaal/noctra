package agent

import (
	"slices"
	"testing"
)

func TestNew_SelectsBackend(t *testing.T) {
	cases := map[string]string{
		"":        "claude", // default
		"claude":  "claude",
		"Claude":  "claude", // case-insensitive
		" codex ": "codex",  // trimmed
		"codex":   "codex",
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

func TestHasRateLimit_PerBackend(t *testing.T) {
	claude, _ := New("claude")
	codex, _ := New("codex")

	// Shared phrasings both backends must catch.
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
}
