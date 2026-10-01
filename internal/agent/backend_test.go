package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNew_SelectsBackend(t *testing.T) {
	cases := map[string]string{
		"":              "claude",
		"claude":        "claude",
		"Claude":        "claude",
		" codex ":       "codex",
		"codex":         "codex",
		"copilot":       "copilot",
		" Copilot ":     "copilot",
		"antigravity":   "antigravity",
		" Antigravity ": "antigravity",
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
	i := slices.Index(args, "-p")
	if i < 0 || i+1 >= len(args) || args[i+1] != "do the thing" {
		t.Errorf("claudeArgs did not pass prompt after -p: %v", args)
	}
	if of := slices.Index(args, "--output-format"); of < 0 || of+1 >= len(args) || args[of+1] != "json" {
		t.Errorf("claudeArgs must request --output-format json: %v", args)
	}
}

func TestClaudeStreamArgs_UsesStreamJSONWithVerbose(t *testing.T) {
	args := claudeStreamArgs(RunOptions{Prompt: "do the thing"})
	if of := slices.Index(args, "--output-format"); of < 0 || of+1 >= len(args) || args[of+1] != "stream-json" {
		t.Errorf("claudeStreamArgs must request --output-format stream-json: %v", args)
	}
	if !slices.Contains(args, "--verbose") {
		t.Errorf("claudeStreamArgs missing --verbose (required by stream-json): %v", args)
	}
	i := slices.Index(args, "-p")
	if i < 0 || i+1 >= len(args) || args[i+1] != "do the thing" {
		t.Errorf("claudeStreamArgs did not pass prompt after -p: %v", args)
	}
}

func TestClaudeUsageTotal_SumsAllTokenBuckets(t *testing.T) {
	u := claudeUsage{InputTokens: 10, OutputTokens: 20, CacheCreationInputTokens: 5, CacheReadInputTokens: 100}
	if got, want := u.total(), int64(135); got != want {
		t.Errorf("claudeUsage.total() = %d, want %d", got, want)
	}
}

func TestTailWorthy_KeepsFailureSignalsDropsConversation(t *testing.T) {
	cases := []struct {
		name string
		ev   claudeStreamEvent
		want bool
	}{
		{"system api_retry", claudeStreamEvent{Type: "system", Subtype: "api_retry"}, true},
		{"system init", claudeStreamEvent{Type: "system", Subtype: "init"}, false},
		{"assistant", claudeStreamEvent{Type: "assistant"}, false},
		{"user", claudeStreamEvent{Type: "user"}, false},
		{"result with text", claudeStreamEvent{Type: "result", Result: "done"}, false},
		{"error result without text", claudeStreamEvent{Type: "result"}, true},
		{"unknown type", claudeStreamEvent{Type: "surprise"}, true},
	}
	for _, tc := range cases {
		if got := tailWorthy(tc.ev); got != tc.want {
			t.Errorf("tailWorthy(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStreamTail_BoundsSizeKeepingNewest(t *testing.T) {
	var tail streamTail
	line := bytes.Repeat([]byte("x"), 1024)
	for i := 0; i < 100; i++ {
		tail.add(append([]byte(fmt.Sprintf("%03d-", i)), line...))
	}
	if tail.size > streamTailMax {
		t.Errorf("tail size %d exceeds cap %d", tail.size, streamTailMax)
	}
	s := tail.String()
	if !strings.Contains(s, "099-") {
		t.Error("tail dropped the newest line")
	}
	if strings.Contains(s, "000-") {
		t.Error("tail kept the oldest line past the cap")
	}
}

func TestStreamTail_KeepsSingleOversizedLine(t *testing.T) {
	var tail streamTail
	tail.add(bytes.Repeat([]byte("y"), streamTailMax+1))
	if len(tail.lines) != 1 {
		t.Errorf("oversized single line must survive, got %d lines", len(tail.lines))
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
	if args[len(args)-1] != "do the thing" {
		t.Errorf("codexArgs should end with the prompt: %v", args)
	}
}

func TestCopilotArgs_UsesAllowAllToolsAndPromptFlag(t *testing.T) {
	args := copilotArgs(RunOptions{Prompt: "do the thing"})
	if !slices.Contains(args, "--allow-all-tools") {
		t.Errorf("copilotArgs missing --allow-all-tools: %v", args)
	}
	if !slices.Contains(args, "--no-ask-user") {
		t.Errorf("copilotArgs missing --no-ask-user: %v", args)
	}
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
	i := slices.Index(args, "--print")
	if i < 0 || i+1 >= len(args) || args[i+1] != "do the thing" || args[len(args)-1] != "do the thing" {
		t.Errorf("antigravityArgs did not pass prompt as the value of --print: %v", args)
	}
	if skip := slices.Index(args, "--dangerously-skip-permissions"); skip > i {
		t.Errorf("--dangerously-skip-permissions must come before --print: %v", args)
	}
}

func TestCopilotEnv_SkipsWhenCopilotTokenAlreadySet(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "already-here")
	if env := copilotEnv(context.Background()); env != nil {
		t.Errorf("expected nil (inherit os.Environ) when COPILOT_GITHUB_TOKEN is set, got %v", env)
	}
}

func TestCopilotEnv_PinsAmbientUserTokenForCopilot(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gho_usertoken")
	env := copilotEnv(context.Background())
	if !slices.Contains(env, "COPILOT_GITHUB_TOKEN=gho_usertoken") {
		t.Errorf("expected the user token pinned as COPILOT_GITHUB_TOKEN, got %v", env)
	}
}

func TestCopilotEnv_InjectsGhAuthTokenWhenNoneSet(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = token ]; then echo faketoken123; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	env := copilotEnv(context.Background())
	if env == nil {
		t.Fatal("expected env with injected COPILOT_GITHUB_TOKEN, got nil")
	}
	if !slices.Contains(env, "COPILOT_GITHUB_TOKEN=faketoken123") {
		t.Errorf("expected COPILOT_GITHUB_TOKEN=faketoken123 in env, got %v", env)
	}
}

func TestCopilotEnv_SkipsClassicPAT(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

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

	codexOnly := []string{
		"Error: rate_limit_exceeded",
		"You have exceeded your current quota",
	}
	for _, in := range codexOnly {
		if !codex.HasRateLimit(in) {
			t.Errorf("codex.HasRateLimit(%q) = false, want true", in)
		}
	}

	copilotOnly := []string{
		"Error: rate_limit_exceeded",
		"You have exceeded your current quota",
	}
	for _, in := range copilotOnly {
		if !copilot.HasRateLimit(in) {
			t.Errorf("copilot.HasRateLimit(%q) = false, want true", in)
		}
	}

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

func TestChildEnv_MergesRunEnvOverParent(t *testing.T) {
	t.Setenv("GH_TOKEN", "personal")
	if got := childEnv(nil, RunOptions{}); got != nil {
		t.Fatalf("no extra env should keep inheriting, got %d vars", len(got))
	}
	got := childEnv(nil, RunOptions{Env: []string{"GH_TOKEN=scoped"}})
	if slices.Contains(got, "GH_TOKEN=personal") || !slices.Contains(got, "GH_TOKEN=scoped") {
		t.Fatal("run env must replace the parent's GH_TOKEN")
	}
	got = childEnv([]string{"A=1", "GH_TOKEN=x"}, RunOptions{Env: []string{"GH_TOKEN=scoped"}})
	if !slices.Equal(got, []string{"A=1", "GH_TOKEN=scoped"}) {
		t.Fatalf("backend env must be kept and overridden, got %v", got)
	}
}
