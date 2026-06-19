package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// baseCLIs are the external commands Noctra always shells out to,
// regardless of which coding-agent backend is selected. The agent CLI itself
// (claude / codex) is appended per-backend — see AgentCLI / RequiredCLIs.
var baseCLIs = []string{"git", "gh"}

// agentCLIs maps a backend name to the CLI binary it requires on PATH.
var agentCLIs = map[string]string{
	"claude":  "claude",
	"codex":   "codex",
	"copilot": "copilot",
}

// DefaultConfigDir returns the per-user config directory (~/.noctra/).
// This is where .env and logs/ live when Noctra is installed globally
// (go install / prebuilt binary). The cwd-checkout override in resolveScriptDir
// still takes precedence during development.
func DefaultConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".noctra")
}

// legacyPathMigrations maps each old Nightshift home-relative path to its
// Noctra replacement. Nightshift was renamed to Noctra (ENG-204); a live
// instance may still hold all of its state (PR cursor, cloned repos,
// worktrees, .env, logs) under the old ~/.nightshift* locations. We rename
// them in place on startup so the upgrade doesn't lose that state.
var legacyPathMigrations = [][2]string{
	{".nightshift", ".noctra"},                       // config dir (.env + logs/)
	{".nightshift-repos", ".noctra-repos"},           // clone cache
	{".nightshift-worktrees", ".noctra-worktrees"},   // per-ticket worktrees
	{".nightshift-state.json", ".noctra-state.json"}, // legacy JSON state store
}

// MigrateLegacyPaths renames any surviving ~/.nightshift* paths to their
// ~/.noctra* equivalents, but only when the old path exists and the new one
// does not — so it's a safe no-op on fresh installs and on already-migrated
// hosts, and never clobbers newer state. It's best-effort: a failed rename is
// logged to stderr and does not abort startup.
func MigrateLegacyPaths() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	for _, m := range legacyPathMigrations {
		oldPath := filepath.Join(home, m[0])
		newPath := filepath.Join(home, m[1])
		if _, err := os.Stat(oldPath); err != nil {
			continue // old path absent — nothing to migrate
		}
		if _, err := os.Stat(newPath); !os.IsNotExist(err) {
			continue // new path already exists, or stat failed — don't clobber
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not migrate %s -> %s: %v\n", oldPath, newPath, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "migrated legacy path %s -> %s\n", oldPath, newPath)
	}
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
	DefaultGeminiMode       = "api"
	DefaultGeminiModel      = "gemini-2.5-pro"
	DefaultMaxReviewRetries = 1

	// Auto-iterate (ENG-173) — disabled by default; opt in via .env.
	DefaultMaxPRIterations = 3
	DefaultPRPollInterval  = 2 * time.Minute

	// Auto-release-label (ENG-231) — disabled by default; opt in via .env.
	DefaultReleaseBump = "patch"

	// Budget / cost-aware management (ENG-217).
	DefaultRateLimitStrategy = "pause"
	DefaultRateLimitCooldown = 30 * time.Minute

	// Sweep — autonomous maintenance (ENG-222) — disabled by default.
	DefaultSweepInterval = 24 * time.Hour
	DefaultSweepMaxTasks = 5
)

// Config is Noctra's resolved runtime configuration.
type Config struct {
	// Linear
	LinearAPIKey            string
	LinearOAuthToken        string
	LinearOAuthClientID     string
	LinearOAuthClientSecret string
	LinearOAuthRefreshToken string
	LinearOAuthScope        string
	LinearTeamKey           string
	TriggerMode             string // "state" (default) or "label"
	TriggerState            string // watched column name (state mode)
	TriggerLabel            string // label name to watch (label mode)
	InReviewState           string

	// Repos
	RepoPath   string // optional single-repo fallback for unmapped projects
	MainBranch string

	// Agent
	AgentBackend  string // coding-agent CLI: "claude" (default), "codex", or "copilot"
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
	GeminiMode       string
	GeminiAPIKey     string
	GeminiModel      string
	MaxReviewRetries int

	// Auto-iterate on PR review feedback (ENG-173) — off by default.
	AutoIteratePRs   bool
	MaxPRIterations  int
	PRPollInterval   time.Duration
	TrustedReviewers []string // GitHub logins/bots Noctra will act on (default: humans only)
	StateDB          string   // SQLite state database path
	StateFile        string   // legacy JSON state file migration source

	// Auto-release-label (ENG-231) — off by default. When enabled, Noctra
	// applies a release:* label at PR creation derived from the agent's
	// RELEASE: line in its output.
	AutoReleaseLabel   bool
	DefaultReleaseBump string // "patch" (default), "minor", or "major"

	// Budget / cost-aware management (ENG-217).
	MaxDailyTokens    int64         // daily token cap, 0 = unlimited
	MaxDailyUSD       float64       // daily dollar cap, 0 = unlimited
	RateLimitStrategy string        // "pause" (default) or "shutdown"
	RateLimitCooldown time.Duration // pause duration after rate limit (default 30m)

	// Sweep — autonomous maintenance (ENG-222) — off by default.
	SweepEnabled  bool          // opt-in maintenance sweep scheduler
	SweepSchedule string        // cron expression (e.g. "0 2 * * *"); empty = use SweepInterval
	SweepInterval time.Duration // fallback fixed interval when no cron (default 24h)
	SweepMaxTasks int           // max tasks per sweep run (default 5)
	SweepTasks    []string      // enabled task names (nil = all registered tasks)

	// Derived paths
	ScriptDir    string
	EnvFile      string
	ReposBase    string
	WorktreeBase string
	LogDir       string
}

// Load resolves config from .env (in scriptDir) and the process environment. To
// match Noctra's bash predecessor, values declared in .env take precedence
// over the process environment.
func Load(scriptDir string) (*Config, error) {
	// Rename any surviving ~/.nightshift* state into ~/.noctra* before we
	// resolve paths, so an upgraded instance keeps its cursor/worktrees/repos.
	MigrateLegacyPaths()

	envFile := filepath.Join(scriptDir, ".env")
	fileEnv, err := LoadEnvFile(envFile)
	if err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()

	reposBase := getenv(fileEnv, "REPOS_BASE", filepath.Join(home, ".noctra-repos"))

	cfg := &Config{
		LinearAPIKey:            getenv(fileEnv, "LINEAR_API_KEY", ""),
		LinearOAuthToken:        getenv(fileEnv, "LINEAR_OAUTH_TOKEN", ""),
		LinearOAuthClientID:     getenv(fileEnv, "LINEAR_OAUTH_CLIENT_ID", ""),
		LinearOAuthClientSecret: getenv(fileEnv, "LINEAR_OAUTH_CLIENT_SECRET", ""),
		LinearOAuthRefreshToken: getenv(fileEnv, "LINEAR_OAUTH_REFRESH_TOKEN", ""),
		LinearOAuthScope:        getenv(fileEnv, "LINEAR_OAUTH_SCOPE", ""),
		LinearTeamKey:           getenv(fileEnv, "LINEAR_TEAM_KEY", DefaultLinearTeamKey),
		TriggerMode:             strings.ToLower(getenv(fileEnv, "TRIGGER_MODE", DefaultTriggerMode)),
		TriggerState:            getenv(fileEnv, "TRIGGER_STATE", DefaultTriggerState),
		TriggerLabel:            getenv(fileEnv, "TRIGGER_LABEL", ""),
		InReviewState:           getenv(fileEnv, "IN_REVIEW_STATE", DefaultInReviewState),

		RepoPath:   getenv(fileEnv, "REPO_PATH", ""),
		MainBranch: getenv(fileEnv, "MAIN_BRANCH", DefaultMainBranch),

		AgentBackend:  strings.ToLower(strings.TrimSpace(getenv(fileEnv, "AGENT_BACKEND", DefaultAgentBackend))),
		UseAgentTeams: getbool(fileEnv, "USE_AGENT_TEAMS", false),

		TelegramEnabled:  getbool(fileEnv, "TELEGRAM_ENABLED", false),
		TelegramBotToken: getenv(fileEnv, "TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:   getenv(fileEnv, "TELEGRAM_CHAT_ID", ""),
		TelegramVerbose:  getbool(fileEnv, "TELEGRAM_VERBOSE", false),

		GeminiMode:   strings.ToLower(strings.TrimSpace(getenv(fileEnv, "GEMINI_MODE", DefaultGeminiMode))),
		GeminiAPIKey: getenv(fileEnv, "GEMINI_API_KEY", ""),
		GeminiModel:  getenv(fileEnv, "GEMINI_MODEL", DefaultGeminiModel),

		ScriptDir:    scriptDir,
		EnvFile:      envFile,
		ReposBase:    reposBase,
		WorktreeBase: getenv(fileEnv, "WORKTREE_BASE", filepath.Join(home, ".noctra-worktrees")),
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
	cfg.StateFile = getenv(fileEnv, "STATE_FILE", filepath.Join(home, ".noctra-state.json"))
	cfg.StateDB = getenv(fileEnv, "STATE_DB", filepath.Join(DefaultConfigDir(), "state.db"))

	// Auto-release-label
	cfg.AutoReleaseLabel = getbool(fileEnv, "AUTO_RELEASE_LABEL", false)
	cfg.DefaultReleaseBump = strings.ToLower(strings.TrimSpace(getenv(fileEnv, "DEFAULT_RELEASE_BUMP", DefaultReleaseBump)))

	// Budget / cost-aware management (ENG-217)
	cfg.MaxDailyTokens = int64(getint(fileEnv, "MAX_DAILY_TOKENS", 0))
	cfg.MaxDailyUSD = getfloat(fileEnv, "MAX_DAILY_USD", 0)
	cfg.RateLimitStrategy = strings.ToLower(strings.TrimSpace(getenv(fileEnv, "RATE_LIMIT_STRATEGY", DefaultRateLimitStrategy)))
	cooldownSecs := getint(fileEnv, "RATE_LIMIT_COOLDOWN", int(DefaultRateLimitCooldown/time.Second))
	cfg.RateLimitCooldown = time.Duration(cooldownSecs) * time.Second

	// Sweep — autonomous maintenance (ENG-222)
	cfg.SweepEnabled = getbool(fileEnv, "SWEEP_ENABLED", false)
	cfg.SweepSchedule = getenv(fileEnv, "SWEEP_SCHEDULE", "")
	sweepIntervalSecs := getint(fileEnv, "SWEEP_INTERVAL", int(DefaultSweepInterval/time.Second))
	cfg.SweepInterval = time.Duration(sweepIntervalSecs) * time.Second
	cfg.SweepMaxTasks = getint(fileEnv, "SWEEP_MAX_TASKS", DefaultSweepMaxTasks)
	cfg.SweepTasks = getlist(fileEnv, "SWEEP_TASKS")

	return cfg, nil
}

// Validate checks that required fields are set. It does NOT require REPO_PATH:
// repos are resolved per-ticket from each Linear project's "Repo:" directive,
// with REPO_PATH as an optional single-repo fallback — so a directive-only setup
// is valid, and a ticket that resolves to nothing is skipped gracefully with a
// Linear comment (not a startup failure).
func (c *Config) Validate() error {
	var errs []string

	if c.LinearAPIKey == "" && c.LinearOAuthToken == "" && !c.ActorAppConfigured() {
		errs = append(errs, "LINEAR_API_KEY (or LINEAR_OAUTH_TOKEN) is required — run ./noctra setup or set it in .env")
	}

	if _, ok := agentCLIs[c.AgentBackend]; !ok {
		errs = append(errs, fmt.Sprintf("AGENT_BACKEND must be \"claude\", \"codex\", or \"copilot\", got %q", c.AgentBackend))
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

	switch c.GeminiMode {
	case "api", "cli":
	default:
		errs = append(errs, fmt.Sprintf("GEMINI_MODE must be \"api\" or \"cli\", got %q", c.GeminiMode))
	}

	if c.AutoReleaseLabel {
		switch c.DefaultReleaseBump {
		case "patch", "minor", "major":
		default:
			errs = append(errs, fmt.Sprintf("DEFAULT_RELEASE_BUMP must be \"patch\", \"minor\", or \"major\", got %q", c.DefaultReleaseBump))
		}
	}

	switch c.RateLimitStrategy {
	case "pause", "shutdown":
	default:
		errs = append(errs, fmt.Sprintf("RATE_LIMIT_STRATEGY must be \"pause\" or \"shutdown\", got %q", c.RateLimitStrategy))
	}

	if c.RepoPath != "" && !isGitRepo(c.RepoPath) {
		errs = append(errs, fmt.Sprintf("REPO_PATH (%s) is not a git repository", c.RepoPath))
	}

	// No REPO_PATH is fine: repos come from Linear project "Repo:" directives at
	// dispatch time. An unresolvable ticket is skipped per-ticket with a Linear
	// comment, so this isn't a startup-fatal condition.

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func (c *Config) ActorAppConfigured() bool {
	return c.LinearOAuthClientID != "" && c.LinearOAuthClientSecret != ""
}

func (c *Config) OAuthPartiallyConfigured() bool {
	return (c.LinearOAuthClientID != "") != (c.LinearOAuthClientSecret != "")
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

// RequiredCLIs returns the external commands Noctra relies on for the
// configured backend: the always-on base set plus the selected agent CLI.
func (c *Config) RequiredCLIs() []string {
	clis := make([]string, 0, len(baseCLIs)+1)
	clis = append(clis, baseCLIs...)
	clis = append(clis, c.AgentCLI())
	return clis
}

// AllCandidateCLIs returns the base CLIs plus every agent backend's CLI.
// Used by the doctor to surface missing backends that per-ticket label
// selection could request at runtime (e.g. "agent:codex" on a ticket when
// only claude is installed).
func (c *Config) AllCandidateCLIs() []string {
	seen := map[string]bool{}
	clis := make([]string, 0, len(baseCLIs)+len(agentCLIs))
	for _, cli := range baseCLIs {
		if !seen[cli] {
			clis = append(clis, cli)
			seen[cli] = true
		}
	}
	// Collect agent CLIs into a sorted slice so the output order is
	// deterministic (Go map iteration is randomized).
	sorted := make([]string, 0, len(agentCLIs))
	for _, cli := range agentCLIs {
		sorted = append(sorted, cli)
	}
	slices.Sort(sorted)
	for _, cli := range sorted {
		if !seen[cli] {
			clis = append(clis, cli)
			seen[cli] = true
		}
	}
	return clis
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

func getfloat(fileEnv map[string]string, key string, def float64) float64 {
	v := getenv(fileEnv, key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
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
