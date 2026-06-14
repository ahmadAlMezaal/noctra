package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noctraEnvKeys are every env var Noctra reads. Tests clear them all
// up front so the dev's shell environment (direnv, exported .env, etc.) can't
// leak through and quietly satisfy a check the test means to fail.
var noctraEnvKeys = []string{
	"LINEAR_API_KEY", "LINEAR_TEAM_KEY", "TRIGGER_MODE", "TRIGGER_STATE",
	"TRIGGER_LABEL", "IN_REVIEW_STATE",
	"REPO_PATH", "MAIN_BRANCH",
	"MAX_CONCURRENT", "POLL_INTERVAL", "USE_AGENT_TEAMS",
	"MAX_DISPATCHES", "MAX_RETRIES", "AGENT_TIMEOUT_MINUTES",
	"TELEGRAM_ENABLED", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "TELEGRAM_VERBOSE",
	"GEMINI_MODE", "GEMINI_API_KEY", "GEMINI_MODEL", "MAX_REVIEW_RETRIES",
	"REPOS_BASE", "WORKTREE_BASE", "LOG_DIR",
	"AUTO_ITERATE_PRS", "MAX_PR_ITERATIONS", "PR_POLL_INTERVAL",
	"TRUSTED_REVIEWERS", "STATE_FILE",
}

func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range noctraEnvKeys {
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
}

func TestLoad_TelegramVerbose(t *testing.T) {
	isolateEnv(t)

	t.Run("default false when absent", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
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

func TestValidate_AllowsDirectiveOnlySetup(t *testing.T) {
	isolateEnv(t)

	// No REPO_PATH is valid: repos are routed per-ticket from each Linear
	// project's "Repo:" directive. Validate must not fail on this.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("directive-only setup should validate, got: %v", err)
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

func TestLoad_AutoIterateDefaults(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AutoIteratePRs {
		t.Errorf("AutoIteratePRs should default to false")
	}
	if cfg.MaxPRIterations != DefaultMaxPRIterations {
		t.Errorf("MaxPRIterations: got %d, want %d", cfg.MaxPRIterations, DefaultMaxPRIterations)
	}
	if cfg.PRPollInterval != DefaultPRPollInterval {
		t.Errorf("PRPollInterval: got %v, want %v", cfg.PRPollInterval, DefaultPRPollInterval)
	}
	if cfg.TrustedReviewers != nil {
		t.Errorf("TrustedReviewers should default to nil (= humans only), got %v", cfg.TrustedReviewers)
	}
	if cfg.StateFile == "" {
		t.Error("StateFile should have a default path")
	}
}

func TestLoad_TrustedReviewersParsesCSV(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
TRUSTED_REVIEWERS="gemini-code-assist, coderabbit,humanreviewer"
AUTO_ITERATE_PRS="true"
MAX_PR_ITERATIONS="5"
PR_POLL_INTERVAL="60"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.AutoIteratePRs {
		t.Error("AutoIteratePRs should be true")
	}
	if cfg.MaxPRIterations != 5 {
		t.Errorf("MaxPRIterations: got %d", cfg.MaxPRIterations)
	}
	if cfg.PRPollInterval != 60*time.Second {
		t.Errorf("PRPollInterval: got %v", cfg.PRPollInterval)
	}
	want := []string{"gemini-code-assist", "coderabbit", "humanreviewer"}
	if len(cfg.TrustedReviewers) != len(want) {
		t.Fatalf("TrustedReviewers length: got %d, want %d (%v)", len(cfg.TrustedReviewers), len(want), cfg.TrustedReviewers)
	}
	for i, w := range want {
		if cfg.TrustedReviewers[i] != w {
			t.Errorf("TrustedReviewers[%d]: got %q, want %q", i, cfg.TrustedReviewers[i], w)
		}
	}
}

func TestDefaultConfigDir(t *testing.T) {
	dir := DefaultConfigDir()
	if dir == "" {
		t.Fatal("DefaultConfigDir returned empty string")
	}
	if !strings.HasSuffix(dir, ".noctra") {
		t.Errorf("DefaultConfigDir = %q, want suffix .noctra", dir)
	}
}

func TestLoad_LogDirDefaultsToLogs(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "logs")
	if cfg.LogDir != want {
		t.Errorf("LogDir = %q, want %q", cfg.LogDir, want)
	}
}

func TestLoad_TriggerModeDefaultsToState(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TriggerMode != "state" {
		t.Errorf("TriggerMode: got %q, want \"state\"", cfg.TriggerMode)
	}
	if cfg.TriggerLabel != "" {
		t.Errorf("TriggerLabel should default to empty, got %q", cfg.TriggerLabel)
	}
}

func TestLoad_TriggerModeLabel(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TRIGGER_MODE="label"
TRIGGER_LABEL="noctra"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TriggerMode != "label" {
		t.Errorf("TriggerMode: got %q, want \"label\"", cfg.TriggerMode)
	}
	if cfg.TriggerLabel != "noctra" {
		t.Errorf("TriggerLabel: got %q, want \"noctra\"", cfg.TriggerLabel)
	}
}

func TestLoad_TriggerModeCaseInsensitive(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TRIGGER_MODE="Label"
TRIGGER_LABEL="noctra"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TriggerMode != "label" {
		t.Errorf("TriggerMode should be lowercased, got %q", cfg.TriggerMode)
	}
}

func TestValidate_LabelModeRequiresTriggerLabel(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TRIGGER_MODE="label"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "TRIGGER_LABEL") {
		t.Fatalf("expected TRIGGER_LABEL error, got %v", err)
	}
}

func TestValidate_LabelModePassesWithLabel(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TRIGGER_MODE="label"
TRIGGER_LABEL="noctra"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidate_InvalidTriggerModeRejected(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TRIGGER_MODE="magic"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "TRIGGER_MODE") {
		t.Fatalf("expected TRIGGER_MODE error, got %v", err)
	}
}

func TestValidate_StateModeDoesNotRequireTriggerLabel(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
TRIGGER_MODE="state"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate should pass in state mode without TRIGGER_LABEL: %v", err)
	}
}

func TestLoad_AgentBackendDefaultsToClaude(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentBackend != "claude" {
		t.Errorf("AgentBackend: got %q, want \"claude\"", cfg.AgentBackend)
	}
	if cfg.AgentCLI() != "claude" {
		t.Errorf("AgentCLI: got %q, want \"claude\"", cfg.AgentCLI())
	}
	if got := cfg.RequiredCLIs(); got[len(got)-1] != "claude" {
		t.Errorf("RequiredCLIs should end with the agent CLI, got %v", got)
	}
}

func TestLoad_GeminiModeDefaultsToAPI(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GeminiMode != DefaultGeminiMode {
		t.Errorf("GeminiMode: got %q, want %q", cfg.GeminiMode, DefaultGeminiMode)
	}
}

func TestLoad_GeminiModeLowercased(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
GEMINI_MODE="CLI"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GeminiMode != "cli" {
		t.Errorf("GeminiMode should be lowercased, got %q", cfg.GeminiMode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate should pass with cli mode: %v", err)
	}
}

func TestValidate_RejectsUnknownGeminiMode(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
GEMINI_MODE="other"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "GEMINI_MODE") {
		t.Fatalf("expected GEMINI_MODE error, got %v", err)
	}
}

func TestLoad_AgentBackendCodexLowercased(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
AGENT_BACKEND="Codex"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentBackend != "codex" {
		t.Errorf("AgentBackend should be lowercased, got %q", cfg.AgentBackend)
	}
	if cfg.AgentCLI() != "codex" {
		t.Errorf("AgentCLI: got %q, want \"codex\"", cfg.AgentCLI())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate should pass with codex backend: %v", err)
	}
}

func TestLoad_AgentBackendCopilotLowercased(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
AGENT_BACKEND="Copilot"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentBackend != "copilot" {
		t.Errorf("AgentBackend should be lowercased, got %q", cfg.AgentBackend)
	}
	if cfg.AgentCLI() != "copilot" {
		t.Errorf("AgentCLI: got %q, want \"copilot\"", cfg.AgentCLI())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate should pass with copilot backend: %v", err)
	}
}

func TestValidate_InvalidAgentBackendRejected(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `
LINEAR_API_KEY="lin_xyz"
AGENT_BACKEND="gemini"
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "AGENT_BACKEND") {
		t.Fatalf("expected AGENT_BACKEND error, got %v", err)
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

func TestMigrateLegacyPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Old Nightshift state present; new Noctra counterparts absent.
	if err := os.MkdirAll(filepath.Join(home, ".nightshift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nightshift", ".env"), []byte("X=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nightshift-worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nightshift-state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A counterpart that ALREADY exists must not be clobbered.
	if err := os.MkdirAll(filepath.Join(home, ".nightshift-repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".noctra-repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".noctra-repos", "keep"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	MigrateLegacyPaths()

	// Migrated: new path exists, old path gone, contents preserved.
	if b, err := os.ReadFile(filepath.Join(home, ".noctra", ".env")); err != nil || string(b) != "X=1" {
		t.Errorf("config dir not migrated: %v / %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(home, ".nightshift")); !os.IsNotExist(err) {
		t.Errorf("old config dir should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".noctra-worktrees")); err != nil {
		t.Errorf("worktrees not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".noctra-state.json")); err != nil {
		t.Errorf("state file not migrated: %v", err)
	}

	// Not clobbered: existing .noctra-repos kept its content; old left in place.
	if _, err := os.Stat(filepath.Join(home, ".noctra-repos", "keep")); err != nil {
		t.Errorf("existing .noctra-repos was clobbered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".nightshift-repos")); err != nil {
		t.Errorf("old .nightshift-repos should remain when target exists: %v", err)
	}

	// Idempotent: a second run is a clean no-op.
	MigrateLegacyPaths()
	if b, err := os.ReadFile(filepath.Join(home, ".noctra", ".env")); err != nil || string(b) != "X=1" {
		t.Errorf("second migration disturbed state: %v / %q", err, b)
	}
}
