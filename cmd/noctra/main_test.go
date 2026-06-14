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
	if !strings.Contains(got, "complete -F _noctra noctra") {
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
	if !strings.HasPrefix(got, "#compdef noctra") {
		t.Errorf("zsh script must start with #compdef directive:\n%s", got)
	}
	if !strings.Contains(got, "compdef _noctra noctra") {
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
	if !slices.Contains(args, "--user-unit=noctra.service") {
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
	if !slices.Contains(args, "--no-pager") {
		t.Error("default (no follow) should include --no-pager so the latest line shows without scrolling")
	}
}

func TestLogsArgs_Follow(t *testing.T) {
	args := logsArgs(true)
	if !slices.Contains(args, "--user-unit=noctra.service") {
		t.Error("missing --user-unit flag")
	}
	if !slices.Contains(args, "-f") {
		t.Error("follow mode should include -f")
	}
	if slices.Contains(args, "-n") {
		t.Error("-n should not be present when following")
	}
}

func TestParseUninstallArgs(t *testing.T) {
	tests := []struct {
		name               string
		args               []string
		purge, force, help bool
		wantErr            bool
	}{
		{"no args", nil, false, false, false, false},
		{"purge", []string{"--purge"}, true, false, false, false},
		{"force long", []string{"--force"}, false, true, false, false},
		{"force short", []string{"-y"}, false, true, false, false},
		{"purge + force", []string{"--purge", "--force"}, true, true, false, false},
		{"help long", []string{"--help"}, false, false, true, false},
		{"help short", []string{"-h"}, false, false, true, false},
		{"unknown/typo flag", []string{"--pruge"}, false, false, false, true},
		{"unknown after valid", []string{"--purge", "--nope"}, false, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			purge, force, help, err := parseUninstallArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return // on error the bool returns are unused
			}
			if purge != tt.purge || force != tt.force || help != tt.help {
				t.Errorf("got purge=%v force=%v help=%v; want purge=%v force=%v help=%v",
					purge, force, help, tt.purge, tt.force, tt.help)
			}
		})
	}
}
