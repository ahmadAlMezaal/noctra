package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Defaults — used when a setting is absent from both .env and the process
// environment. Kept as exported constants so tests and the setup wizard can
// reference them.
const (
	DefaultLinearTeamKey    = "ENG"
	DefaultTriggerState     = "Next"
	DefaultInReviewState    = "In Review"
	DefaultMainBranch       = "main"
	DefaultMaxConcurrent    = 3
	DefaultPollInterval     = 30 * time.Second
	DefaultMaxDispatches    = 10
	DefaultMaxRetries       = 3
	DefaultAgentTimeout     = 45 * time.Minute
	DefaultGeminiModel      = "gemini-2.5-pro"
	DefaultMaxReviewRetries = 1
)

// Config is Nightshift's resolved runtime configuration.
type Config struct {
	// Linear
	LinearAPIKey  string
	LinearTeamKey string
	TriggerState  string
	InReviewState string

	// Repos
	RepoPath   string // optional single-repo fallback for unmapped projects
	MainBranch string

	// Agent
	MaxConcurrent int
	PollInterval  time.Duration
	UseAgentTeams bool
	AgentTimeout  time.Duration

	// Safety guards
	MaxDispatches int
	MaxRetries    int

	// Telegram (optional)
	TelegramEnabled  bool
	TelegramBotToken string
	TelegramChatID   string

	// Gemini review gate (optional)
	GeminiAPIKey     string
	GeminiModel      string
	MaxReviewRetries int

	// Derived paths
	ScriptDir    string
	EnvFile      string
	ReposFile    string
	ReposBase    string
	WorktreeBase string
	LogDir       string

	// Loaded registry (may be nil if repos.json is absent)
	Registry *RepoRegistry
}

// Load resolves config from .env (in scriptDir), the process environment, and
// repos.json. To match Nightshift's bash predecessor, values declared in .env
// take precedence over the process environment.
func Load(scriptDir string) (*Config, error) {
	envFile := filepath.Join(scriptDir, ".env")
	fileEnv, err := LoadEnvFile(envFile)
	if err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()

	reposFile := getenv(fileEnv, "REPOS_FILE", filepath.Join(scriptDir, "repos.json"))
	reposBase := getenv(fileEnv, "REPOS_BASE", filepath.Join(home, ".nightshift-repos"))

	cfg := &Config{
		LinearAPIKey:  getenv(fileEnv, "LINEAR_API_KEY", ""),
		LinearTeamKey: getenv(fileEnv, "LINEAR_TEAM_KEY", DefaultLinearTeamKey),
		TriggerState:  getenv(fileEnv, "TRIGGER_STATE", DefaultTriggerState),
		InReviewState: getenv(fileEnv, "IN_REVIEW_STATE", DefaultInReviewState),

		RepoPath:   getenv(fileEnv, "REPO_PATH", ""),
		MainBranch: getenv(fileEnv, "MAIN_BRANCH", DefaultMainBranch),

		UseAgentTeams: getbool(fileEnv, "USE_AGENT_TEAMS", false),

		TelegramEnabled:  getbool(fileEnv, "TELEGRAM_ENABLED", false),
		TelegramBotToken: getenv(fileEnv, "TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:   getenv(fileEnv, "TELEGRAM_CHAT_ID", ""),

		GeminiAPIKey: getenv(fileEnv, "GEMINI_API_KEY", ""),
		GeminiModel:  getenv(fileEnv, "GEMINI_MODEL", DefaultGeminiModel),

		ScriptDir:    scriptDir,
		EnvFile:      envFile,
		ReposFile:    reposFile,
		ReposBase:    reposBase,
		WorktreeBase: getenv(fileEnv, "WORKTREE_BASE", filepath.Join(home, ".nightshift-worktrees")),
		LogDir:       getenv(fileEnv, "LOG_DIR", filepath.Join(scriptDir, ".agent-logs")),
	}

	cfg.MaxConcurrent = getint(fileEnv, "MAX_CONCURRENT", DefaultMaxConcurrent)
	cfg.MaxDispatches = getint(fileEnv, "MAX_DISPATCHES", DefaultMaxDispatches)
	cfg.MaxRetries = getint(fileEnv, "MAX_RETRIES", DefaultMaxRetries)
	cfg.MaxReviewRetries = getint(fileEnv, "MAX_REVIEW_RETRIES", DefaultMaxReviewRetries)

	pollSecs := getint(fileEnv, "POLL_INTERVAL", int(DefaultPollInterval/time.Second))
	cfg.PollInterval = time.Duration(pollSecs) * time.Second

	timeoutMin := getint(fileEnv, "AGENT_TIMEOUT_MINUTES", int(DefaultAgentTimeout/time.Minute))
	cfg.AgentTimeout = time.Duration(timeoutMin) * time.Minute

	cfg.Registry, err = LoadRepoRegistry(reposFile)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that required fields are set and that there's at least one
// usable repo source (registry or REPO_PATH fallback).
func (c *Config) Validate() error {
	var errs []string

	if c.LinearAPIKey == "" {
		errs = append(errs, "LINEAR_API_KEY is required — run ./nightshift setup or set it in .env")
	}

	if c.RepoPath != "" && !isGitRepo(c.RepoPath) {
		errs = append(errs, fmt.Sprintf("REPO_PATH (%s) is not a git repository", c.RepoPath))
	}

	if c.Registry == nil && c.RepoPath == "" {
		errs = append(errs,
			fmt.Sprintf("no repos configured — run ./nightshift setup, create %s, or set REPO_PATH in .env",
				c.ReposFile))
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

// getenv returns the first non-empty value among: fileEnv[key], os.Getenv(key),
// def. This matches the bash predecessor, where `source .env` overrode any
// pre-existing process-env values.
func getenv(fileEnv map[string]string, key, def string) string {
	if v, ok := fileEnv[key]; ok && v != "" {
		return v
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getbool(fileEnv map[string]string, key string, def bool) bool {
	v := getenv(fileEnv, key, "")
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes", "y":
		return true
	case "false", "0", "no", "n":
		return false
	}
	return def
}

func getint(fileEnv map[string]string, key string, def int) int {
	v := getenv(fileEnv, key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
