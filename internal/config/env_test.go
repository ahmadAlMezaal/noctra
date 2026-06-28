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

func TestPatchEnvFile_UpsertsExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("# header\nKEY_A=old\nKEY_B=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := PatchEnvFile(path, map[string]string{"KEY_A": "new"}); err != nil {
		t.Fatalf("PatchEnvFile: %v", err)
	}

	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got["KEY_A"] != "new" {
		t.Errorf("KEY_A: got %q, want %q", got["KEY_A"], "new")
	}
	if got["KEY_B"] != "keep" {
		t.Errorf("KEY_B: got %q, want %q (should be preserved)", got["KEY_B"], "keep")
	}
}

func TestPatchEnvFile_AppendsNewKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("EXISTING=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := PatchEnvFile(path, map[string]string{"NEW_KEY": "added"}); err != nil {
		t.Fatalf("PatchEnvFile: %v", err)
	}

	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got["EXISTING"] != "value" {
		t.Errorf("EXISTING: got %q, want %q", got["EXISTING"], "value")
	}
	if got["NEW_KEY"] != "added" {
		t.Errorf("NEW_KEY: got %q, want %q", got["NEW_KEY"], "added")
	}
}

func TestPatchEnvFile_PreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	original := "# Important comment\nKEY=old\n# Another comment\nOTHER=val\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := PatchEnvFile(path, map[string]string{"KEY": "new"}); err != nil {
		t.Fatalf("PatchEnvFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !containsLine(content, "# Important comment") {
		t.Error("first comment was lost")
	}
	if !containsLine(content, "# Another comment") {
		t.Error("second comment was lost")
	}
	if !containsLine(content, `KEY="new"`) {
		t.Errorf("KEY not updated, got:\n%s", content)
	}
	if !containsLine(content, "OTHER=val") {
		t.Error("OTHER should be preserved unchanged")
	}
}

func TestPatchEnvFile_CreatesFileFromScratch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	if err := PatchEnvFile(path, map[string]string{"BRAND_NEW": "yes"}); err != nil {
		t.Fatalf("PatchEnvFile: %v", err)
	}

	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got["BRAND_NEW"] != "yes" {
		t.Errorf("BRAND_NEW: got %q, want %q", got["BRAND_NEW"], "yes")
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions: got %o, want 0600", perm)
	}
}

func TestPatchEnvFile_EmptyUpdatesNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("KEY=val\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := PatchEnvFile(path, map[string]string{}); err != nil {
		t.Fatalf("PatchEnvFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "KEY=val\n" {
		t.Errorf("file changed on empty updates: %q", string(data))
	}
}

func TestPatchEnvFile_AtomicMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	if err := PatchEnvFile(path, map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions: got %o, want 0600", perm)
	}
}

func TestPatchEnvFile_PreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// Simulate a .env with a hand-added key the wizard doesn't manage.
	original := `LINEAR_API_KEY="lin_abc"
LINEAR_OAUTH_TOKEN="lin_oauth_secret"
LINEAR_TEAM_KEY="ENG"
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Patch only LINEAR_API_KEY — LINEAR_OAUTH_TOKEN must survive.
	if err := PatchEnvFile(path, map[string]string{
		"LINEAR_API_KEY": "lin_new",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["LINEAR_API_KEY"] != "lin_new" {
		t.Errorf("LINEAR_API_KEY: got %q, want %q", got["LINEAR_API_KEY"], "lin_new")
	}
	if got["LINEAR_OAUTH_TOKEN"] != "lin_oauth_secret" {
		t.Errorf("LINEAR_OAUTH_TOKEN was lost: got %q", got["LINEAR_OAUTH_TOKEN"])
	}
	if got["LINEAR_TEAM_KEY"] != "ENG" {
		t.Errorf("LINEAR_TEAM_KEY was lost: got %q", got["LINEAR_TEAM_KEY"])
	}
}

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}
