package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	content := `# comment line
LINEAR_API_KEY=lin_abc
LINEAR_TEAM_KEY="ENG"

# Quoted value containing a space — preserved
IN_REVIEW_STATE="In Review"
SINGLE_QUOTED='Yes'
EMPTY=
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}

	want := map[string]string{
		"LINEAR_API_KEY":  "lin_abc",
		"LINEAR_TEAM_KEY": "ENG",
		"IN_REVIEW_STATE": "In Review",
		"SINGLE_QUOTED":   "Yes",
		"EMPTY":           "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadEnvFile_MissingFile(t *testing.T) {
	got, err := LoadEnvFile(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestLoadEnvFile_InvalidLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("VALID=ok\nbad line without equals\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvFile(path); err == nil {
		t.Fatal("expected error for line missing '='")
	}
}
