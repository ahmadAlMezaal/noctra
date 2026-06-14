package main

import (
	"slices"
	"strings"
	"testing"
)

func TestCompletionScript_Bash(t *testing.T) {
	got, err := completionScript("bash")
	if err != nil {
		t.Fatalf("bash: unexpected error: %v", err)
	}
	if !strings.Contains(got, "complete -F _nightshift nightshift") {
		t.Errorf("bash script missing complete registration:\n%s", got)
	}
	// Every subcommand must appear in the completion word list.
	for _, c := range subcommands {
		if !strings.Contains(got, c) {
			t.Errorf("bash script missing subcommand %q", c)
		}
	}
}

func TestCompletionScript_Zsh(t *testing.T) {
	got, err := completionScript("zsh")
	if err != nil {
		t.Fatalf("zsh: unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "#compdef nightshift") {
		t.Errorf("zsh script must start with #compdef directive:\n%s", got)
	}
	if !strings.Contains(got, "compdef _nightshift nightshift") {
		t.Errorf("zsh script missing compdef registration:\n%s", got)
	}
	for _, c := range subcommands {
		if !strings.Contains(got, c) {
			t.Errorf("zsh script missing subcommand %q", c)
		}
	}
}

func TestCompletionScript_UnknownShell(t *testing.T) {
	if _, err := completionScript("fish"); err == nil {
		t.Error("expected error for unsupported shell, got nil")
	}
	if _, err := completionScript(""); err == nil {
		t.Error("expected error for empty shell, got nil")
	}
}

func TestLogsArgs_Default(t *testing.T) {
	args := logsArgs(false)
	if !slices.Contains(args, "--user-unit=nightshift.service") {
		t.Error("missing --user-unit flag")
	}
	if !slices.Contains(args, "-n") {
		t.Fatal("default (no follow) should include -n")
	}
	nIdx := slices.Index(args, "-n")
	if nIdx+1 >= len(args) || args[nIdx+1] != "200" {
		t.Errorf("expected -n 200, got args: %v", args)
	}
	if slices.Contains(args, "-f") {
		t.Error("-f should not be present when follow is false")
	}
}

func TestLogsArgs_Follow(t *testing.T) {
	args := logsArgs(true)
	if !slices.Contains(args, "--user-unit=nightshift.service") {
		t.Error("missing --user-unit flag")
	}
	if !slices.Contains(args, "-f") {
		t.Error("follow mode should include -f")
	}
	if slices.Contains(args, "-n") {
		t.Error("-n should not be present when following")
	}
}
