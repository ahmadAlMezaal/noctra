package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// baseCLIs are the external commands Nightshift always shells out to,
// regardless of which coding-agent backend is selected. The agent CLI itself
// (claude / codex) is appended per-backend — see AgentCLI / RequiredCLIs.
var baseCLIs = []string{"git", "gh"}

// agentCLIs maps a backend name to the CLI binary it requires on PATH.
var agentCLIs = map[string]string{
	"claude": "claude",
	"codex":  "codex",
}

// DefaultConfigDir returns the per-user config directory (~/.nightshift/).
// This is where .env, repos.json, and logs/ live when Nightshift is installed
// globally (go install / prebuilt binary). The cwd-checkout override in
// resolveScriptDir still takes precedence during development.
func DefaultConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nightshift")
}

// Defaults — used when a setting is absent from both .env and the process
// environment. Kept as exported constants so tests and the setup wizard can
// reference them.
const (
	DefaultLinearTeamKey    = "ENG"
	DefaultAgentBackend     = "claude"
	DefaultTriggerMode      = "state"
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

	// Auto-iterate (ENG-173) — disabled by default; opt in via .env.
	DefaultMaxPRIterations = 3
	DefaultPRPollInterval  = 2 * time.Minute
)

// Config is Nightshift's resolved runtime configuration.
type Config struct {
	// Linear
	LinearAPIKey  string
	LinearTeamKey string
	TriggerMode   string // "state" (default) or "label"
	TriggerState  string // watched column name (state mode)
	TriggerLabel  string // label name to watch (label mode)
	InReviewState string

	// Repos
	RepoPath   string // optional single-repo fallback for unmapped projects
	MainBranch string

	// Agent
	AgentBackend  string // coding-agent CLI: "claude" (default) or "codex"
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
	TelegramVerbose  bool // also notify on dispatch (otherwise: terminal events only)

	// Gemini review gate (optional)
	GeminiAPIKey     string
	GeminiModel      string
	MaxReviewRetries int

	// Auto-iterate on PR review feedback (ENG-173) — off by default.
	AutoIteratePRs   bool
	MaxPRIterations  int
	PRPollInterval   time.Duration
	TrustedReviewers []string // GitHub logins/bots Nightshift will act on (default: humans only)
	StateFile        string   // where the per-PR cursor + iteration count is persisted

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
		TriggerMode:   strings.ToLower(getenv(fileEnv, "TRIGGER_MODE", DefaultTriggerMode)),
		TriggerState:  getenv(fileEnv, "TRIGGER_STATE", DefaultTriggerState),
		TriggerLabel:  getenv(fileEnv, "TRIGGER_LABEL", ""),
		InReviewState: getenv(fileEnv, "IN_REVIEW_STATE", DefaultInReviewState),

		RepoPath:   getenv(fileEnv, "REPO_PATH", ""),
		MainBranch: getenv(fileEnv, "MAIN_BRANCH", DefaultMainBranch),

		AgentBackend:  strings.ToLower(getenv(fileEnv, "AGENT_BACKEND", DefaultAgentBackend)),
		UseAgentTeams: getbool(fileEnv, "USE_AGENT_TEAMS", false),

		TelegramEnabled:  getbool(fileEnv, "TELEGRAM_ENABLED", false),
		TelegramBotToken: getenv(fileEnv, "TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:   getenv(fileEnv, "TELEGRAM_CHAT_ID", ""),
		TelegramVerbose:  getbool(fileEnv, "TELEGRAM_VERBOSE", false),

		GeminiAPIKey: getenv(fileEnv, "GEMINI_API_KEY", ""),
		GeminiModel:  getenv(fileEnv, "GEMINI_MODEL", DefaultGeminiModel),

		ScriptDir:    scriptDir,
		EnvFile:      envFile,
		ReposFile:    reposFile,
		ReposBase:    reposBase,
		WorktreeBase: getenv(fileEnv, "WORKTREE_BASE", filepath.Join(home, ".nightshift-worktrees")),
		LogDir:       getenv(fileEnv, "LOG_DIR", filepath.Join(scriptDir, "logs")),
	}

	cfg.MaxConcurrent = getint(fileEnv, "MAX_CONCURRENT", DefaultMaxConcurrent)
	cfg.MaxDispatches = getint(fileEnv, "MAX_DISPATCHES", DefaultMaxDispatches)
	cfg.MaxRetries = getint(fileEnv, "MAX_RETRIES", DefaultMaxRetries)
	cfg.MaxReviewRetries = getint(fileEnv, "MAX_REVIEW_RETRIES", DefaultMaxReviewRetries)

	pollSecs := getint(fileEnv, "POLL_INTERVAL", int(DefaultPollInterval/time.Second))
	cfg.PollInterval = time.Duration(pollSecs) * time.Second

	timeoutMin := getint(fileEnv, "AGENT_TIMEOUT_MINUTES", int(DefaultAgentTimeout/time.Minute))
	cfg.AgentTimeout = time.Duration(timeoutMin) * time.Minute

	// Auto-iterate
	cfg.AutoIteratePRs = getbool(fileEnv, "AUTO_ITERATE_PRS", false)
	cfg.MaxPRIterations = getint(fileEnv, "MAX_PR_ITERATIONS", DefaultMaxPRIterations)
	prPollSecs := getint(fileEnv, "PR_POLL_INTERVAL", int(DefaultPRPollInterval/time.Second))
	cfg.PRPollInterval = time.Duration(prPollSecs) * time.Second
	cfg.TrustedReviewers = getlist(fileEnv, "TRUSTED_REVIEWERS")
	cfg.StateFile = getenv(fileEnv, "STATE_FILE", filepath.Join(home, ".nightshift-state.json"))

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

	if _, ok := agentCLIs[c.AgentBackend]; !ok {
		errs = append(errs, fmt.Sprintf("AGENT_BACKEND must be \"claude\" or \"codex\", got %q", c.AgentBackend))
	}

	switch c.TriggerMode {
	case "state":
		// Default mode — no extra validation needed (trigger state is always set via default).
	case "label":
		if c.TriggerLabel == "" {
			errs = append(errs, "TRIGGER_LABEL is required when TRIGGER_MODE=label")
		}
	default:
		errs = append(errs, fmt.Sprintf("TRIGGER_MODE must be \"state\" or \"label\", got %q", c.TriggerMode))
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

// AgentCLI returns the CLI binary the configured backend requires on PATH.
// Falls back to the default backend's CLI when AgentBackend is unset/unknown
// so callers (e.g. the doctor pre-config scan) still get a sane value.
func (c *Config) AgentCLI() string {
	if cli, ok := agentCLIs[c.AgentBackend]; ok {
		return cli
	}
	return agentCLIs[DefaultAgentBackend]
}

// RequiredCLIs returns the external commands Nightshift relies on for the
// configured backend: the always-on base set plus the selected agent CLI.
func (c *Config) RequiredCLIs() []string {
	return append(append([]string(nil), baseCLIs...), c.AgentCLI())
}

// CheckCLIs returns the subset of RequiredCLIs that are missing from PATH.
// `Validate` hard-errors on config; this is the softer PATH check used by
// `run`/`cleanup` and the setup wizard's status block.
func (c *Config) CheckCLIs() (missing []string) {
	for _, cmd := range c.RequiredCLIs() {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, cmd)
		}
	}
	return missing
}

// AgentCLIs returns the set of every backend's CLI binary, keyed by backend
// name. Exposed so the doctor/wizard can render hints without a loaded Config.
func AgentCLIs() map[string]string {
	out := make(map[string]string, len(agentCLIs))
	for k, v := range agentCLIs {
		out[k] = v
	}
	return out
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

// getlist parses a comma-separated value into a trimmed, empty-filtered slice.
// Returns nil (not an empty slice) when the value is absent, which lets callers
// distinguish "no entries configured" from "configured to empty list."
func getlist(fileEnv map[string]string, key string) []string {
	v := getenv(fileEnv, key, "")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
