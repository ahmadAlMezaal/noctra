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

var baseCLIs = []string{"git", "gh"}

var agentCLIs = map[string]string{
	"claude":      "claude",
	"codex":       "codex",
	"copilot":     "copilot",
	"antigravity": "agy",
}

func DefaultConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".noctra")
}

var legacyPathMigrations = [][2]string{
	{".nightshift", ".noctra"},
	{".nightshift-repos", ".noctra-repos"},
	{".nightshift-worktrees", ".noctra-worktrees"},
	{".nightshift-state.json", ".noctra-state.json"},
}

func MigrateLegacyPaths() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	for _, m := range legacyPathMigrations {
		oldPath := filepath.Join(home, m[0])
		newPath := filepath.Join(home, m[1])
		if _, err := os.Stat(oldPath); err != nil {
			continue
		}
		if _, err := os.Stat(newPath); !os.IsNotExist(err) {
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not migrate %s -> %s: %v\n", oldPath, newPath, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "migrated legacy path %s -> %s\n", oldPath, newPath)
	}
}

const (
	DefaultLinearTeamKey    = "ENG"
	DefaultTicketSources    = "linear"
	DefaultAgentBackend     = "claude"
	DefaultTriggerMode      = "state"
	DefaultTriggerState     = "Next"
	DefaultInReviewState    = "In Review"
	DefaultDoneState        = "Done"
	DefaultMainBranch       = "main"
	DefaultMaxConcurrent    = 3
	DefaultPollInterval     = 30 * time.Second
	DefaultMaxDispatches    = 40
	DefaultMaxRetries       = 3
	DefaultAgentTimeout     = 45 * time.Minute
	DefaultAuthServiceURL   = "https://auth.getnoctra.dev"
	DefaultGeminiMode       = "api"
	DefaultGeminiModel      = "gemini-2.5-pro"
	DefaultMaxReviewRetries = 1

	DefaultMaxPRIterations = 3
	DefaultPRPollInterval  = 2 * time.Minute

	DefaultReleaseBump = "patch"

	DefaultRateLimitStrategy = "pause"
	DefaultRateLimitCooldown = 30 * time.Minute

	DefaultSweepInterval  = 24 * time.Hour
	DefaultSweepMaxTasks  = 5
	DefaultSweepTimeout   = 20 * time.Minute
	DefaultSweepMaxTokens = 2_000_000

	DefaultJiraInReviewStatus = "In Review"

	DefaultPlanConfirmLabel = "plan-first"

	SuggestedTrustedReviewer = "chatgpt-codex-connector"
)

type Config struct {
	TicketSources      []string
	GitHubIssuesRepos  []string
	GitHubTriggerLabel string

	JiraBaseURL        string
	JiraUserEmail      string
	JiraAPIToken       string
	JiraProject        string
	JiraTriggerStatus  string
	JiraTriggerLabel   string
	JiraInReviewStatus string

	LinearAPIKey            string
	LinearOAuthToken        string
	LinearOAuthClientID     string
	LinearOAuthClientSecret string
	LinearOAuthRefreshToken string
	LinearOAuthScope        string
	LinearTeamKey           string
	TriggerMode             string
	TriggerState            string
	TriggerLabel            string
	InReviewState           string
	DoneState               string

	RepoPath   string
	MainBranch string

	AgentBackend  string
	MaxConcurrent int
	PollInterval  time.Duration
	UseAgentTeams bool
	AgentTimeout  time.Duration

	MaxDispatches int
	MaxRetries    int

	TelegramEnabled  bool
	TelegramBotToken string
	TelegramChatID   string

	SlackWebhookURL string

	DiscordWebhookURL string

	VerboseNotifications bool

	GeminiMode       string
	GeminiAPIKey     string
	GeminiModel      string
	MaxReviewRetries int

	AutoIteratePRs   bool
	MaxPRIterations  int
	PRPollInterval   time.Duration
	TrustedReviewers []string
	StateDB          string
	StateFile        string

	AutoReleaseLabel   bool
	DefaultReleaseBump string

	MaxDailyTokens    int64
	MaxDailyUSD       float64
	AgentMaxTokens    int64
	RateLimitStrategy string
	RateLimitCooldown time.Duration

	SweepEnabled  bool
	SweepSchedule string
	SweepInterval time.Duration
	SweepMaxTasks int
	SweepTimeout  time.Duration
	SweepTasks    []string
	SweepRepos    []string

	PlanConfirm      bool
	PlanConfirmLabel string

	DashboardAddr       string
	DashboardToken      string
	DashboardAdminToken string
	DashboardSSH        string

	GitHubAuthMode string
	AuthServiceURL string
	GitHubAuthDir  string

	ScriptDir    string
	EnvFile      string
	ReposBase    string
	WorktreeBase string
	LogDir       string
}

func Load(scriptDir string) (*Config, error) {
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
		DoneState:               getenv(fileEnv, "DONE_STATE", DefaultDoneState),
		GitHubTriggerLabel:      getenv(fileEnv, "GITHUB_TRIGGER_LABEL", ""),

		RepoPath:   getenv(fileEnv, "REPO_PATH", ""),
		MainBranch: getenv(fileEnv, "MAIN_BRANCH", DefaultMainBranch),

		AgentBackend:  strings.ToLower(strings.TrimSpace(getenv(fileEnv, "AGENT_BACKEND", DefaultAgentBackend))),
		UseAgentTeams: getbool(fileEnv, "USE_AGENT_TEAMS", false),

		TelegramEnabled:  getbool(fileEnv, "TELEGRAM_ENABLED", false),
		TelegramBotToken: getenv(fileEnv, "TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:   getenv(fileEnv, "TELEGRAM_CHAT_ID", ""),

		VerboseNotifications: verboseNotifications(fileEnv),

		SlackWebhookURL: getenv(fileEnv, "SLACK_WEBHOOK_URL", ""),

		DiscordWebhookURL: getenv(fileEnv, "DISCORD_WEBHOOK_URL", ""),

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
	cfg.TicketSources = ticketSources(fileEnv)
	cfg.GitHubIssuesRepos = getlist(fileEnv, "GITHUB_ISSUES_REPOS")
	if cfg.GitHubTriggerLabel == "" {
		cfg.GitHubTriggerLabel = cfg.TriggerLabel
	}

	cfg.JiraBaseURL = getenv(fileEnv, "JIRA_BASE_URL", "")
	cfg.JiraUserEmail = getenv(fileEnv, "JIRA_USER_EMAIL", "")
	cfg.JiraAPIToken = getenv(fileEnv, "JIRA_API_TOKEN", "")
	cfg.JiraProject = getenv(fileEnv, "JIRA_PROJECT", "")
	cfg.JiraTriggerStatus = getenv(fileEnv, "JIRA_TRIGGER_STATUS", "")
	cfg.JiraTriggerLabel = getenv(fileEnv, "JIRA_TRIGGER_LABEL", "")
	cfg.JiraInReviewStatus = getenv(fileEnv, "JIRA_IN_REVIEW_STATUS", DefaultJiraInReviewStatus)

	pollSecs := getint(fileEnv, "POLL_INTERVAL", int(DefaultPollInterval/time.Second))
	cfg.PollInterval = time.Duration(pollSecs) * time.Second

	timeoutMin := getint(fileEnv, "AGENT_TIMEOUT_MINUTES", int(DefaultAgentTimeout/time.Minute))
	cfg.AgentTimeout = time.Duration(timeoutMin) * time.Minute

	cfg.AutoIteratePRs = getbool(fileEnv, "AUTO_ITERATE_PRS", false)
	cfg.MaxPRIterations = getint(fileEnv, "MAX_PR_ITERATIONS", DefaultMaxPRIterations)
	prPollSecs := getint(fileEnv, "PR_POLL_INTERVAL", int(DefaultPRPollInterval/time.Second))
	cfg.PRPollInterval = time.Duration(prPollSecs) * time.Second
	cfg.TrustedReviewers = getlist(fileEnv, "TRUSTED_REVIEWERS")
	cfg.StateFile = getenv(fileEnv, "STATE_FILE", filepath.Join(home, ".noctra-state.json"))
	cfg.StateDB = getenv(fileEnv, "STATE_DB", filepath.Join(DefaultConfigDir(), "state.db"))

	cfg.AutoReleaseLabel = getbool(fileEnv, "AUTO_RELEASE_LABEL", false)
	cfg.DefaultReleaseBump = strings.ToLower(strings.TrimSpace(getenv(fileEnv, "DEFAULT_RELEASE_BUMP", DefaultReleaseBump)))

	cfg.MaxDailyTokens = int64(getint(fileEnv, "MAX_DAILY_TOKENS", 0))
	cfg.MaxDailyUSD = getfloat(fileEnv, "MAX_DAILY_USD", 0)
	cfg.AgentMaxTokens = int64(getint(fileEnv, "AGENT_MAX_TOKENS", 0))
	cfg.RateLimitStrategy = strings.ToLower(strings.TrimSpace(getenv(fileEnv, "RATE_LIMIT_STRATEGY", DefaultRateLimitStrategy)))
	cooldownSecs := getint(fileEnv, "RATE_LIMIT_COOLDOWN", int(DefaultRateLimitCooldown/time.Second))
	cfg.RateLimitCooldown = time.Duration(cooldownSecs) * time.Second

	cfg.SweepEnabled = getbool(fileEnv, "SWEEP_ENABLED", false)
	cfg.SweepSchedule = getenv(fileEnv, "SWEEP_SCHEDULE", "")
	sweepIntervalSecs := getint(fileEnv, "SWEEP_INTERVAL", int(DefaultSweepInterval/time.Second))
	cfg.SweepInterval = time.Duration(sweepIntervalSecs) * time.Second
	cfg.SweepMaxTasks = getint(fileEnv, "SWEEP_MAX_TASKS", DefaultSweepMaxTasks)
	sweepTimeoutMin := getint(fileEnv, "SWEEP_TIMEOUT_MINUTES", int(DefaultSweepTimeout/time.Minute))
	cfg.SweepTimeout = time.Duration(sweepTimeoutMin) * time.Minute
	cfg.SweepTasks = getlist(fileEnv, "SWEEP_TASKS")
	cfg.SweepRepos = getlist(fileEnv, "SWEEP_REPOS")

	cfg.PlanConfirm = getbool(fileEnv, "PLAN_CONFIRM", false)
	cfg.PlanConfirmLabel = getenv(fileEnv, "PLAN_CONFIRM_LABEL", DefaultPlanConfirmLabel)

	cfg.DashboardAddr = getenv(fileEnv, "DASHBOARD_ADDR", "")
	cfg.DashboardToken = getenv(fileEnv, "DASHBOARD_TOKEN", "")
	cfg.DashboardAdminToken = getenv(fileEnv, "DASHBOARD_ADMIN_TOKEN", "")
	cfg.DashboardSSH = getenv(fileEnv, "DASHBOARD_SSH", "")

	cfg.GitHubAuthMode = strings.ToLower(strings.TrimSpace(getenv(fileEnv, "GITHUB_AUTH_MODE", "auto")))
	cfg.AuthServiceURL = strings.TrimRight(getenv(fileEnv, "NOCTRA_AUTH_URL", DefaultAuthServiceURL), "/")
	cfg.GitHubAuthDir = getenv(fileEnv, "GITHUB_AUTH_DIR", filepath.Join(DefaultConfigDir(), "github"))

	return cfg, nil
}

func (c *Config) Validate() error {
	var errs []string
	sources := c.TicketSources
	if len(sources) == 0 {
		sources = []string{"linear"}
	}

	switch c.GitHubAuthMode {
	case "", "auto", "app", "token":
	default:
		errs = append(errs, fmt.Sprintf("GITHUB_AUTH_MODE must be auto, app or token (got %q)", c.GitHubAuthMode))
	}

	if usesSource(sources, "linear") && c.LinearAPIKey == "" && c.LinearOAuthToken == "" && !c.ActorAppConfigured() {
		errs = append(errs, "LINEAR_API_KEY (or LINEAR_OAUTH_TOKEN) is required — run ./noctra setup or set it in .env")
	}

	if _, ok := agentCLIs[c.AgentBackend]; !ok {
		errs = append(errs, fmt.Sprintf("AGENT_BACKEND must be \"claude\", \"codex\", \"copilot\", or \"antigravity\", got %q", c.AgentBackend))
	}

	switch c.TriggerMode {
	case "state":
	case "label":
		if c.TriggerLabel == "" {
			errs = append(errs, "TRIGGER_LABEL is required when TRIGGER_MODE=label")
		}
	default:
		errs = append(errs, fmt.Sprintf("TRIGGER_MODE must be \"state\" or \"label\", got %q", c.TriggerMode))
	}

	for _, src := range sources {
		switch src {
		case "linear", "github", "jira":
		default:
			errs = append(errs, fmt.Sprintf("TICKET_SOURCES entries must be \"linear\", \"github\", or \"jira\", got %q", src))
		}
	}
	if usesSource(sources, "github") {
		if len(c.GitHubIssuesRepos) == 0 {
			errs = append(errs, "GITHUB_ISSUES_REPOS is required when TICKET_SOURCES includes github")
		}
		if c.GitHubTriggerLabel == "" {
			errs = append(errs, "GITHUB_TRIGGER_LABEL or TRIGGER_LABEL is required when TICKET_SOURCES includes github")
		}
	}
	if usesSource(sources, "jira") {
		if c.JiraBaseURL == "" {
			errs = append(errs, "JIRA_BASE_URL is required when TICKET_SOURCES includes jira")
		}
		if c.JiraUserEmail == "" {
			errs = append(errs, "JIRA_USER_EMAIL is required when TICKET_SOURCES includes jira")
		}
		if c.JiraAPIToken == "" {
			errs = append(errs, "JIRA_API_TOKEN is required when TICKET_SOURCES includes jira")
		}
		if c.JiraProject == "" {
			errs = append(errs, "JIRA_PROJECT is required when TICKET_SOURCES includes jira")
		}
		if c.JiraTriggerStatus == "" && c.JiraTriggerLabel == "" {
			errs = append(errs, "JIRA_TRIGGER_STATUS or JIRA_TRIGGER_LABEL is required when TICKET_SOURCES includes jira")
		}
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

func (c *Config) UsesTicketSource(name string) bool {
	sources := c.TicketSources
	if len(sources) == 0 {
		sources = []string{"linear"}
	}
	return usesSource(sources, name)
}

func usesSource(sources []string, name string) bool {
	for _, src := range sources {
		if src == name {
			return true
		}
	}
	return false
}

func (c *Config) AgentCLI() string {
	if cli, ok := agentCLIs[c.AgentBackend]; ok {
		return cli
	}
	return agentCLIs[DefaultAgentBackend]
}

func (c *Config) RequiredCLIs() []string {
	clis := make([]string, 0, len(baseCLIs)+1)
	clis = append(clis, baseCLIs...)
	clis = append(clis, c.AgentCLI())
	return clis
}

func (c *Config) AllCandidateCLIs() []string {
	seen := map[string]bool{}
	clis := make([]string, 0, len(baseCLIs)+len(agentCLIs))
	for _, cli := range baseCLIs {
		if !seen[cli] {
			clis = append(clis, cli)
			seen[cli] = true
		}
	}
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

func (c *Config) CheckCLIs() (missing []string) {
	for _, cmd := range c.RequiredCLIs() {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, cmd)
		}
	}
	return missing
}

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

func getenv(fileEnv map[string]string, key, def string) string {
	if v, ok := fileEnv[key]; ok && v != "" {
		return v
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func verboseNotifications(fileEnv map[string]string) bool {
	if getenv(fileEnv, "VERBOSE_NOTIFICATIONS", "") != "" {
		return getbool(fileEnv, "VERBOSE_NOTIFICATIONS", false)
	}
	if getenv(fileEnv, "TELEGRAM_VERBOSE", "") != "" {
		fmt.Fprintln(os.Stderr, "warning: TELEGRAM_VERBOSE is deprecated; rename it to VERBOSE_NOTIFICATIONS")
		return getbool(fileEnv, "TELEGRAM_VERBOSE", false)
	}
	return false
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

func ticketSources(fileEnv map[string]string) []string {
	v := getenv(fileEnv, "TICKET_SOURCES", "")
	if v == "" {
		v = getenv(fileEnv, "TICKET_SOURCE", DefaultTicketSources)
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		out = append(out, p)
		seen[p] = true
	}
	return out
}
