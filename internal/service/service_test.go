package service

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitFile(t *testing.T) {
	out := unitFile("/home/u/.local/bin/noctra", "/home/u/.local/bin:/usr/bin")

	for _, want := range []string{
		"ExecStart=/home/u/.local/bin/noctra run",
		"Environment=PATH=/home/u/.local/bin:/usr/bin",
		"Description=Noctra — autonomous Linear→PR agent",
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit file missing %q\n---\n%s", want, out)
		}
	}
}

func TestPurgePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := PurgePaths()
	if err != nil {
		t.Fatalf("PurgePaths: %v", err)
	}

	want := []string{
		filepath.Join(home, ".noctra"),
		filepath.Join(home, ".noctra-repos"),
		filepath.Join(home, ".noctra-worktrees"),
		filepath.Join(home, ".noctra-state.json"),
	}
	if len(got) != len(want) {
		t.Fatalf("PurgePaths returned %d paths, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("PurgePaths[%d] = %q, want %q", i, got[i], w)
		}
	}
}
