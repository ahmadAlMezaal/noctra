package service

import (
	"os"
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

func TestInstalledBinaryPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No unit yet → empty (caller falls back to the running exe).
	if got := installedBinaryPath(); got != "" {
		t.Errorf("installedBinaryPath with no unit = %q, want \"\"", got)
	}

	// Write a unit and confirm the ExecStart binary (sans the "run" arg) is parsed.
	dest, err := unitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	const wantExe = "/home/u/.local/bin/noctra"
	if err := os.WriteFile(dest, []byte(unitFile(wantExe, "/usr/bin")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := installedBinaryPath(); got != wantExe {
		t.Errorf("installedBinaryPath = %q, want %q", got, wantExe)
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
