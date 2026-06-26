package configcmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/config"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunGet_ExistingKey(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	writeTestFile(t, envFile, `LINEAR_API_KEY="lin_abc"
AGENT_BACKEND="claude"
`)

	if err := runGet(envFile, "LINEAR_API_KEY"); err != nil {
		t.Errorf("runGet should succeed for existing key: %v", err)
	}
}

func TestRunGet_MissingKey(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	writeTestFile(t, envFile, `LINEAR_API_KEY="lin_abc"`)

	if err := runGet(envFile, "NONEXISTENT"); err == nil {
		t.Error("runGet should return an error for a missing key")
	}
}

func TestRunGet_MissingFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	// Missing file: LoadEnvFile returns empty map, key not found.
	if err := runGet(envFile, "ANY"); err == nil {
		t.Error("runGet should return an error when key is not in a missing file")
	}
}

func TestRunSet_KeyEqualsValue(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	writeTestFile(t, envFile, `LINEAR_API_KEY="lin_abc"`)

	if err := runSet(envFile, []string{"LINEAR_API_KEY=lin_new"}); err != nil {
		t.Fatalf("runSet: %v", err)
	}

	got, err := config.LoadEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got["LINEAR_API_KEY"] != "lin_new" {
		t.Errorf("LINEAR_API_KEY: got %q, want %q", got["LINEAR_API_KEY"], "lin_new")
	}
}

func TestRunSet_KeySpaceValue(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	writeTestFile(t, envFile, `AGENT_BACKEND="claude"`)

	if err := runSet(envFile, []string{"AGENT_BACKEND", "codex"}); err != nil {
		t.Fatalf("runSet: %v", err)
	}

	got, err := config.LoadEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got["AGENT_BACKEND"] != "codex" {
		t.Errorf("AGENT_BACKEND: got %q, want %q", got["AGENT_BACKEND"], "codex")
	}
}

func TestRunSet_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")

	if err := runSet(envFile, []string{"NEW_KEY=hello"}); err != nil {
		t.Fatalf("runSet: %v", err)
	}

	got, err := config.LoadEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got["NEW_KEY"] != "hello" {
		t.Errorf("NEW_KEY: got %q, want %q", got["NEW_KEY"], "hello")
	}
}

func TestRunSet_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	writeTestFile(t, envFile, `# config
LINEAR_API_KEY="lin_abc"
LINEAR_OAUTH_TOKEN="oauth_secret"
`)

	if err := runSet(envFile, []string{"LINEAR_API_KEY=lin_new"}); err != nil {
		t.Fatalf("runSet: %v", err)
	}

	got, err := config.LoadEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got["LINEAR_OAUTH_TOKEN"] != "oauth_secret" {
		t.Errorf("LINEAR_OAUTH_TOKEN was lost: got %q", got["LINEAR_OAUTH_TOKEN"])
	}
}

func TestRunPath(t *testing.T) {
	if err := runPath("/some/path/.env"); err != nil {
		t.Errorf("runPath: %v", err)
	}
}

func TestParseKeyValue_KeyEqualsValue(t *testing.T) {
	k, v, err := parseKeyValue([]string{"FOO=bar"})
	if err != nil {
		t.Fatal(err)
	}
	if k != "FOO" || v != "bar" {
		t.Errorf("got key=%q val=%q", k, v)
	}
}

func TestParseKeyValue_KeyEqualsEmpty(t *testing.T) {
	k, v, err := parseKeyValue([]string{"FOO="})
	if err != nil {
		t.Fatal(err)
	}
	if k != "FOO" || v != "" {
		t.Errorf("got key=%q val=%q", k, v)
	}
}

func TestParseKeyValue_TwoArgs(t *testing.T) {
	k, v, err := parseKeyValue([]string{"FOO", "bar"})
	if err != nil {
		t.Fatal(err)
	}
	if k != "FOO" || v != "bar" {
		t.Errorf("got key=%q val=%q", k, v)
	}
}

func TestParseKeyValue_EmptyKey(t *testing.T) {
	_, _, err := parseKeyValue([]string{"=value"})
	if err == nil {
		t.Error("expected error for empty key")
	}
}

func TestParseKeyValue_NoArgs(t *testing.T) {
	_, _, err := parseKeyValue(nil)
	if err == nil {
		t.Error("expected error for no args")
	}
}

func TestParseKeyValue_SingleArgNoEquals(t *testing.T) {
	_, _, err := parseKeyValue([]string{"JUST_A_KEY"})
	if err == nil {
		t.Error("expected error for single arg without '='")
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	if err := Run(t.TempDir(), []string{"bogus"}); err == nil {
		t.Error("expected error for unknown subcommand")
	}
}

func TestRun_NoArgs(t *testing.T) {
	if err := Run(t.TempDir(), nil); err != nil {
		t.Errorf("no-args should print usage, not error: %v", err)
	}
}

func TestRun_Help(t *testing.T) {
	if err := Run(t.TempDir(), []string{"--help"}); err != nil {
		t.Errorf("--help should not error: %v", err)
	}
}

func TestRun_GetMissingArg(t *testing.T) {
	if err := Run(t.TempDir(), []string{"get"}); err == nil {
		t.Error("get without a key should error")
	}
}

func TestRun_SetMissingArg(t *testing.T) {
	if err := Run(t.TempDir(), []string{"set"}); err == nil {
		t.Error("set without a key should error")
	}
}

func TestIsSecretKey(t *testing.T) {
	secret := []string{"LINEAR_API_KEY", "LINEAR_OAUTH_CLIENT_SECRET", "GH_TOKEN", "TELEGRAM_BOT_TOKEN", "SLACK_WEBHOOK_URL", "DB_PASSWORD"}
	for _, k := range secret {
		if !isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = false, want true", k)
		}
	}
	plain := []string{"AGENT_BACKEND", "TRIGGER_MODE", "REPO_PATH", "SWEEP_ENABLED", "MAIN_BRANCH"}
	for _, k := range plain {
		if isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = true, want false", k)
		}
	}
}

func TestMaskSecret(t *testing.T) {
	// Short secrets are fully masked — no trailing characters leak.
	if got := maskSecret("short"); got != "••••••" {
		t.Errorf("maskSecret short = %q, want fully masked", got)
	}
	// Long secrets keep a 4-char hint.
	got := maskSecret("lin_api_abcd1234")
	if got != "••••••1234" {
		t.Errorf("maskSecret long = %q, want last-4 hint", got)
	}
}

func TestRunList(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	writeTestFile(t, envFile, `LINEAR_API_KEY="lin_api_abcd1234"
AGENT_BACKEND="claude"
`)
	// Both masked and revealed paths should succeed without error.
	if err := runList(envFile, false); err != nil {
		t.Errorf("runList (masked) should succeed: %v", err)
	}
	if err := runList(envFile, true); err != nil {
		t.Errorf("runList (reveal) should succeed: %v", err)
	}
}

func TestRunList_MissingFile(t *testing.T) {
	if err := runList(filepath.Join(t.TempDir(), "nope.env"), false); err != nil {
		t.Errorf("runList on a missing file should not error: %v", err)
	}
}
