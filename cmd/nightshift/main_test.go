package main

import (
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
