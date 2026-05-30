package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nightshiftEnvKeys are every env var Nightshift reads. Tests clear them all
// up front so the dev's shell environment (direnv, exported .env, etc.) can't
// leak through and quietly satisfy a check the test means to fail.
var nightshiftEnvKeys = []string{
	"LINEAR_API_KEY", "LINEAR_TEAM_KEY", "TRIGGER_STATE", "IN_REVIEW_STATE",
	"REPO_PATH", "MAIN_BRANCH",
	"MAX_CONCURRENT", "POLL_INTERVAL", "USE_AGENT_TEAMS",
	"MAX_DISPATCHES", "MAX_RETRIES", "AGENT_TIMEOUT_MINUTES",
	"TELEGRAM_ENABLED", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "TELEGRAM_VERBOSE",
	"GEMINI_API_KEY", "GEMINI_MODEL", "MAX_REVIEW_RETRIES",
	"REPOS_FILE", "REPOS_BASE", "WORKTREE_BASE", "LOG_DIR",
}

func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range nightshiftEnvKeys {
		t.Setenv(k, "")
	}
}

// writeFile is a small helper used in tests below.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_AppliesDefaultsAndOverrides(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TRIGGER_STATE="Backlog"
MAX_CONCURRENT="7"
POLL_INTERVAL="15"
AGENT_TIMEOUT_MINUTES="60"
`)
	writeFile(t, filepath.Join(dir, "repos.json"), `{"repos": {}}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// From .env
	if cfg.LinearAPIKey != "lin_xyz" {
		t.Errorf("LinearAPIKey: %q", cfg.LinearAPIKey)
	}
	if cfg.TriggerState != "Backlog" {
		t.Errorf("TriggerState: %q", cfg.TriggerState)
	}
	if cfg.MaxConcurrent != 7 {
		t.Errorf("MaxConcurrent: %d", cfg.MaxConcurrent)
	}
	if cfg.PollInterval.Seconds() != 15 {
		t.Errorf("PollInterval: %v", cfg.PollInterval)
	}
	if cfg.AgentTimeout.Minutes() != 60 {
		t.Errorf("AgentTimeout: %v", cfg.AgentTimeout)
	}

	// Defaults (not in .env)
	if cfg.LinearTeamKey != DefaultLinearTeamKey {
		t.Errorf("LinearTeamKey: %q", cfg.LinearTeamKey)
	}
	if cfg.InReviewState != DefaultInReviewState {
		t.Errorf("InReviewState: %q", cfg.InReviewState)
	}
	if cfg.MainBranch != DefaultMainBranch {
		t.Errorf("MainBranch: %q", cfg.MainBranch)
	}

	if cfg.Registry == nil {
		t.Fatal("Registry should be loaded (even when empty)")
	}
}

func TestLoad_TelegramVerbose(t *testing.T) {
	isolateEnv(t)

	t.Run("default false when absent", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
		writeFile(t, filepath.Join(dir, "repos.json"), `{"repos": {}}`)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.TelegramVerbose {
			t.Errorf("TelegramVerbose should default to false")
		}
	})

	t.Run("reads true from .env", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TELEGRAM_VERBOSE="true"
`)
		writeFile(t, filepath.Join(dir, "repos.json"), `{"repos": {}}`)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.TelegramVerbose {
			t.Errorf("TelegramVerbose should be true when .env says true")
		}
	})
}

func TestValidate_RequiresLinearKey(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY=""
REPO_PATH="`+initBareRepo(t)+`"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Fatalf("expected LINEAR_API_KEY error, got %v", err)
	}
}

func TestValidate_RequiresAtLeastOneRepoSource(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "no repos configured") {
		t.Fatalf("expected no-repos error, got %v", err)
	}
}

func TestValidate_PassesWithRegistry(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	writeFile(t, filepath.Join(dir, "repos.json"), `{"repos": {}}`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidate_PassesWithRepoPathFallback(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	bare := initBareRepo(t)
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
REPO_PATH="`+bare+`"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidate_RejectsNonGitRepoPath(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	notARepo := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
REPO_PATH="`+notARepo+`"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("expected not-a-git-repo error, got %v", err)
	}
}

// initBareRepo creates a minimal-looking git repo (just a .git directory) so
// isGitRepo returns true without us shelling out to git in tests.
func initBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
