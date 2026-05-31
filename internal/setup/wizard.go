// Package setup is the interactive wizard that generates .env and repos.json.
// It's the friendlier alternative to hand-editing the config files.
//
// On re-run, every prompt is pre-filled with the value currently in .env (or
// the static default if absent). Press Enter to keep, type to replace. The
// wizard also offers a "manual mode" that just copies the example templates
// into place for users who prefer to edit by hand.
package setup

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
	"github.com/ahmadAlMezaal/nightshift/internal/linear"
	"github.com/ahmadAlMezaal/nightshift/internal/notify"
)

// Run drives the wizard. It writes scriptDir/.env and scriptDir/repos.json.
func Run(scriptDir string) error {
	envFile := filepath.Join(scriptDir, ".env")
	reposFile := filepath.Join(scriptDir, "repos.json")

	existingEnv, _ := config.LoadEnvFile(envFile)
	existingRepos, _ := config.LoadRepoRegistry(reposFile)

	w := &wizard{in: bufio.NewScanner(os.Stdin)}

	fmt.Println()
	fmt.Println("🌙 Nightshift Setup")
	fmt.Println("   Generates .env and repos.json — press Enter to accept [defaults].")
	if len(existingEnv) > 0 {
		fmt.Println("   Existing values from .env are pre-filled in [brackets].")
	}
	fmt.Println()

	// Mode selector (interactive vs manual)
	switch w.chooseMode() {
	case "manual":
		// Reuse the wizard's scanner so we don't risk dropping buffered
		// bytes by constructing a second Scanner on os.Stdin.
		return runManual(scriptDir, w.in)
	}
	fmt.Println()

	w.chooseTracker()
	fmt.Println()
	w.chooseEngine()
	fmt.Println()

	w.printCLIStatus()
	fmt.Println()

	// ── Linear ─────────────────────────────────────────────────────────────────
	fmt.Println("─── Linear ───")
	var linearKey string
	for {
		linearKey = w.askEx("Linear API key", askOpts{
			existing: existingEnv["LINEAR_API_KEY"],
			secret:   true,
			required: true,
		})
		if w.eof || linearKey == "" {
			break
		}
		fmt.Print("  Verifying ... ")
		name, err := pingLinear(linearKey)
		if err == nil {
			fmt.Printf("ok — authenticated as %s\n", name)
			break
		}
		fmt.Println("FAILED")
		fmt.Printf("  ⚠️  %v\n", err)
		if w.confirm("  Save this key anyway?") || w.eof {
			break
		}
		fmt.Println("  Let's try again — or press Ctrl+C to abort.")
	}

	team := w.askEx("Linear team key", askOpts{
		existing: existingEnv["LINEAR_TEAM_KEY"],
		fallback: config.DefaultLinearTeamKey,
	})
	trigger := w.askEx("Trigger state", askOpts{
		existing: existingEnv["TRIGGER_STATE"],
		fallback: config.DefaultTriggerState,
	})
	review := w.askEx("In-review state", askOpts{
		existing: existingEnv["IN_REVIEW_STATE"],
		fallback: config.DefaultInReviewState,
	})
	fmt.Println()

	// ── Repos: registry ────────────────────────────────────────────────────────
	mainBranch := w.askEx("Default main branch", askOpts{
		existing: existingEnv["MAIN_BRANCH"],
		fallback: config.DefaultMainBranch,
	})
	reg := w.collectRepos(mainBranch, existingRepos)

	// ── Optional REPO_PATH fallback ────────────────────────────────────────────
	fmt.Println()
	fmt.Println("─── Single-repo fallback (optional) ───")
	fmt.Println("Used only for tickets whose Linear project is not in repos.json.")
	repoPath := w.askEx("Path to fallback git repo (blank to skip)", askOpts{
		existing: existingEnv["REPO_PATH"],
	})
	if repoPath != "" && !isGitRepo(repoPath) {
		fmt.Printf("  ⚠️  %s does not look like a git repository (no .git directory).\n", repoPath)
		if !w.confirm("  Save it anyway?") {
			repoPath = ""
		}
	}

	// ── Safety limits ──────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("─── Safety limits ───")
	concurrency := w.askInt("Max concurrent tickets", existingEnv["MAX_CONCURRENT"], config.DefaultMaxConcurrent, 1)
	dispatches := w.askInt("Max dispatches per session", existingEnv["MAX_DISPATCHES"], config.DefaultMaxDispatches, 1)
	retries := w.askInt("Max retries per ticket", existingEnv["MAX_RETRIES"], config.DefaultMaxRetries, 1)
	timeoutMin := w.askInt("Agent timeout (minutes)", existingEnv["AGENT_TIMEOUT_MINUTES"], int(config.DefaultAgentTimeout/time.Minute), 5)
	fmt.Println()

	// ── Optional: Auto-iterate on PR feedback ─────────────────────────────────
	autoIterate, maxIter, prPoll, trusted := w.collectAutoIterate(existingEnv)

	// ── Optional: Gemini review gate ───────────────────────────────────────────
	geminiKey := ""
	if existingEnv["GEMINI_API_KEY"] != "" {
		fmt.Println("Gemini review gate is currently enabled.")
		if w.confirm("Keep it enabled?") {
			geminiKey = w.askEx("Gemini API key", askOpts{existing: existingEnv["GEMINI_API_KEY"], secret: true})
		}
	} else if w.confirm("Enable the Gemini review gate?") {
		geminiKey = w.askEx("Gemini API key", askOpts{secret: true})
	}

	// ── Optional: Telegram ─────────────────────────────────────────────────────
	tgEnabled, tgToken, tgChat, tgVerbose := "false", "", "", "false"
	tgWasEnabled := strings.EqualFold(existingEnv["TELEGRAM_ENABLED"], "true")
	tgWasVerbose := strings.EqualFold(existingEnv["TELEGRAM_VERBOSE"], "true")
	prompt := "Enable Telegram notifications?"
	if tgWasEnabled {
		prompt = "Telegram notifications are currently enabled. Keep them?"
	}
	if w.confirm(prompt) {
		tgEnabled = "true"
		tgToken = w.askEx("Telegram bot token", askOpts{existing: existingEnv["TELEGRAM_BOT_TOKEN"], secret: true, required: true})
		tgChat = w.askEx("Telegram chat ID", askOpts{existing: existingEnv["TELEGRAM_CHAT_ID"], required: true})

		verbosePrompt := "Also notify on every dispatch (more chatty)?"
		if tgWasVerbose {
			verbosePrompt = "Verbose notifications are currently on. Keep them?"
		}
		if w.confirm(verbosePrompt) {
			tgVerbose = "true"
		}

		fmt.Print("  Sending test message ... ")
		if err := testTelegram(tgToken, tgChat); err != nil {
			fmt.Println("FAILED")
			fmt.Printf("  ⚠️  %v\n", err)
			if !w.confirm("  Save Telegram config anyway?") {
				tgEnabled, tgToken, tgChat, tgVerbose = "false", "", "", "false"
			}
		} else {
			fmt.Println("ok — check your Telegram!")
		}
	}

	// ── Summary + confirm ──────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("─── Summary ───")
	fmt.Println()
	fmt.Printf("  LINEAR_API_KEY        = %s\n", mask(linearKey))
	fmt.Printf("  LINEAR_TEAM_KEY       = %s\n", team)
	fmt.Printf("  TRIGGER_STATE         = %s\n", trigger)
	fmt.Printf("  IN_REVIEW_STATE       = %s\n", review)
	fmt.Printf("  MAIN_BRANCH           = %s\n", mainBranch)
	if repoPath != "" {
		fmt.Printf("  REPO_PATH             = %s\n", repoPath)
	}
	fmt.Printf("  MAX_CONCURRENT        = %d\n", concurrency)
	fmt.Printf("  MAX_DISPATCHES        = %d\n", dispatches)
	fmt.Printf("  MAX_RETRIES           = %d\n", retries)
	fmt.Printf("  AGENT_TIMEOUT_MINUTES = %d\n", timeoutMin)
	fmt.Printf("  GEMINI_API_KEY        = %s\n", maskOrNone(geminiKey))
	fmt.Printf("  AUTO_ITERATE_PRS      = %s\n", autoIterate)
	if autoIterate == "true" {
		fmt.Printf("  MAX_PR_ITERATIONS     = %d\n", maxIter)
		fmt.Printf("  PR_POLL_INTERVAL      = %d\n", prPoll)
		if trusted == "" {
			fmt.Printf("  TRUSTED_REVIEWERS     = (humans only)\n")
		} else {
			fmt.Printf("  TRUSTED_REVIEWERS     = %s\n", trusted)
		}
	}
	fmt.Printf("  TELEGRAM_ENABLED      = %s\n", tgEnabled)
	if tgEnabled == "true" {
		fmt.Printf("  TELEGRAM_BOT_TOKEN    = %s\n", mask(tgToken))
		fmt.Printf("  TELEGRAM_CHAT_ID      = %s\n", tgChat)
		fmt.Printf("  TELEGRAM_VERBOSE      = %s\n", tgVerbose)
	}
	fmt.Println()
	fmt.Printf("  repos.json: %d project(s)\n", len(reg.Repos))
	for _, name := range reg.ProjectNames() {
		fmt.Printf("    - %s → %s\n", name, reg.Repos[name].URL)
	}
	fmt.Println()
	if !w.confirm("Save to .env and repos.json?") {
		fmt.Println("Setup cancelled — no files changed.")
		return nil
	}

	// ── Write files ────────────────────────────────────────────────────────────
	if err := writeEnvFile(envFile, envValues{
		linearKey:   linearKey,
		team:        team,
		trigger:     trigger,
		review:      review,
		mainBranch:  mainBranch,
		repoPath:    repoPath,
		concurrency: strconv.Itoa(concurrency),
		dispatches:  strconv.Itoa(dispatches),
		retries:     strconv.Itoa(retries),
		timeoutMin:  strconv.Itoa(timeoutMin),
		geminiKey:   geminiKey,
		tgEnabled:   tgEnabled,
		tgToken:     tgToken,
		tgChat:      tgChat,
		tgVerbose:   tgVerbose,
		autoIterate: autoIterate,
		maxIter:     strconv.Itoa(maxIter),
		prPoll:      strconv.Itoa(prPoll),
		trusted:     trusted,
	}); err != nil {
		return fmt.Errorf("write %s: %w", envFile, err)
	}
	if err := writeReposFile(reposFile, reg); err != nil {
		return fmt.Errorf("write %s: %w", reposFile, err)
	}

	count := len(reg.Repos)
	fmt.Println()
	fmt.Printf("✅ Wrote %s\n", envFile)
	fmt.Printf("✅ Wrote %s (%d repo(s))\n", reposFile, count)
	if count == 0 && repoPath == "" {
		fmt.Println("⚠️  No repos registered and no REPO_PATH fallback — Nightshift won't process any tickets yet.")
	}
	fmt.Println()
	fmt.Println("Start Nightshift with: ./nightshift")
	return nil
}

// runManual copies .env.example → .env and repos.example.json → repos.json,
// asking before overwriting either. The caller passes its own scanner so we
// share the same input stream — constructing a second bufio.Scanner on
// os.Stdin would risk losing bytes the first scanner already buffered.
func runManual(scriptDir string, in *bufio.Scanner) error {
	pairs := []struct{ src, dst string }{
		{filepath.Join(scriptDir, ".env.example"), filepath.Join(scriptDir, ".env")},
		{filepath.Join(scriptDir, "repos.example.json"), filepath.Join(scriptDir, "repos.json")},
	}
	for _, p := range pairs {
		if _, err := os.Stat(p.src); err != nil {
			fmt.Printf("⚠️  Template not found: %s — skipping\n", p.src)
			continue
		}
		if _, err := os.Stat(p.dst); err == nil {
			fmt.Print(filepath.Base(p.dst), " already exists — overwrite? [y/N] ")
			if !in.Scan() || !yes(in.Text()) {
				fmt.Println("   kept existing")
				continue
			}
		}
		if err := copyFile(p.src, p.dst); err != nil {
			return fmt.Errorf("copy %s → %s: %w", p.src, p.dst, err)
		}
		fmt.Printf("📄 Created %s\n", p.dst)
	}
	fmt.Println()
	fmt.Println("Edit those files with your values, then run: ./nightshift")
	return nil
}

// ── Wizard mechanics ────────────────────────────────────────────────────────

type wizard struct {
	in  *bufio.Scanner
	eof bool
}

// readLine writes the prompt and reads one line. Once stdin reaches EOF the
// wizard sticks: every subsequent call returns "" without re-prompting, so
// required-loop helpers above terminate cleanly instead of spinning.
func (w *wizard) readLine(prompt string) string {
	if w.eof {
		return ""
	}
	fmt.Print(prompt)
	if !w.in.Scan() {
		w.eof = true
		return ""
	}
	return strings.TrimSpace(w.in.Text())
}

type askOpts struct {
	existing string // value already in .env, if any
	fallback string // static default if no existing
	secret   bool   // mask existing values in the prompt
	required bool   // loop until non-empty
}

// askEx is the workhorse prompt: shows existing value (or fallback) in
// brackets, accepts Enter to keep, type to replace. Required prompts loop
// until they get a value.
func (w *wizard) askEx(label string, opts askOpts) string {
	for {
		display := opts.existing
		if display == "" {
			display = opts.fallback
		}

		var prompt string
		if display == "" {
			prompt = label + ": "
		} else {
			shown := display
			if opts.secret && opts.existing != "" {
				shown = mask(opts.existing) + " — Enter to keep"
			}
			prompt = fmt.Sprintf("%s [%s]: ", label, shown)
		}

		s := w.readLine(prompt)
		if w.eof {
			return s
		}
		if s == "" {
			s = display
		}
		if s == "" && opts.required {
			fmt.Println("  This value is required.")
			continue
		}
		return s
	}
}

func (w *wizard) askInt(label, existing string, fallback, min int) int {
	defaultStr := strconv.Itoa(fallback)
	if existing != "" {
		defaultStr = existing
	}
	for {
		s := w.askEx(label, askOpts{fallback: defaultStr})
		if w.eof {
			// Preserve the existing .env value on unexpected EOF — losing
			// it would silently downgrade the user's config to the factory
			// default on the next re-run.
			if n, err := strconv.Atoi(defaultStr); err == nil {
				return n
			}
			return fallback
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			fmt.Printf("  Not a number: %q\n", s)
			continue
		}
		if n < min {
			fmt.Printf("  Must be at least %d.\n", min)
			continue
		}
		return n
	}
}

func (w *wizard) confirm(prompt string) bool {
	return yes(w.readLine(prompt + " [y/N] "))
}

func (w *wizard) chooseMode() string {
	fmt.Println("How would you like to configure Nightshift?")
	fmt.Println("  1) Interactive setup (guided prompts) — recommended")
	fmt.Println("  2) Manual setup (copies .env.example & repos.example.json — you fill them in)")
	for {
		s := w.askEx("Choose", askOpts{fallback: "1"})
		if w.eof {
			return "interactive"
		}
		switch s {
		case "1":
			return "interactive"
		case "2":
			return "manual"
		default:
			fmt.Println("  Enter 1 or 2.")
		}
	}
}

func (w *wizard) chooseTracker() {
	fmt.Println("Issue tracker:")
	fmt.Println("  1) Linear")
	fmt.Println("  2) Jira             (coming soon)")
	fmt.Println("  3) GitHub Issues    (coming soon)")
	for {
		s := w.askEx("Choose", askOpts{fallback: "1"})
		if w.eof {
			return
		}
		switch s {
		case "1":
			return
		case "2", "3":
			fmt.Println("  ⏳ Not supported yet — Linear only for now.")
		default:
			fmt.Println("  Enter 1, 2, or 3.")
		}
	}
}

func (w *wizard) chooseEngine() {
	fmt.Println("Implementation engine:")
	fmt.Println("  1) Claude Code")
	fmt.Println("  2) Gemini           (coming soon)")
	for {
		s := w.askEx("Choose", askOpts{fallback: "1"})
		if w.eof {
			return
		}
		switch s {
		case "1":
			return
		case "2":
			fmt.Println("  ⏳ Gemini as an engine isn't supported yet — Claude Code only for now.")
		default:
			fmt.Println("  Enter 1 or 2.")
		}
	}
}

func (w *wizard) printCLIStatus() {
	fmt.Println("Required CLIs:")
	for _, cmd := range config.RequiredCLIs() {
		if _, err := exec.LookPath(cmd); err == nil {
			fmt.Printf("  ✅ %s\n", cmd)
		} else {
			fmt.Printf("  ⚠️  %s — not found in PATH (install before running ./nightshift)\n", cmd)
		}
	}
}

// collectAutoIterate prompts for the auto-iterate-PR feature and its safety
// knobs. Returns the values as strings (for the .env template) plus the
// numeric forms used in the summary block.
func (w *wizard) collectAutoIterate(existing map[string]string) (autoIterate string, maxIter int, prPoll int, trusted string) {
	autoIterate = "false"
	maxIter = config.DefaultMaxPRIterations
	prPoll = int(config.DefaultPRPollInterval / time.Second)

	wasEnabled := strings.EqualFold(existing["AUTO_ITERATE_PRS"], "true")
	prompt := "Enable auto-iterate on PR review feedback?"
	if wasEnabled {
		prompt = "Auto-iterate on PR feedback is currently on. Keep it?"
	}
	if !w.confirm(prompt) {
		return autoIterate, maxIter, prPoll, ""
	}
	autoIterate = "true"

	maxIter = w.askInt("Max iterations per PR before stopping",
		existing["MAX_PR_ITERATIONS"], config.DefaultMaxPRIterations, 1)
	prPoll = w.askInt("PR poll interval (seconds)",
		existing["PR_POLL_INTERVAL"], int(config.DefaultPRPollInterval/time.Second), 30)

	fmt.Println("Trusted reviewers (comma-separated GitHub logins).")
	fmt.Println("Leave blank to act on humans only — bots get logged but skipped.")
	trusted = w.askEx("Trusted reviewers", askOpts{existing: existing["TRUSTED_REVIEWERS"]})

	return autoIterate, maxIter, prPoll, trusted
}

func (w *wizard) collectRepos(defaultMainBranch string, existing *config.RepoRegistry) *config.RepoRegistry {
	fmt.Println()
	fmt.Println("─── Repos ───")
	fmt.Println("Map each Linear project to a git repo. Nightshift clones these on demand.")

	reg := &config.RepoRegistry{Repos: map[string]config.RepoEntry{}}
	if existing != nil {
		for k, v := range existing.Repos {
			reg.Repos[k] = v
		}
	}

	if len(reg.Repos) > 0 {
		fmt.Printf("Currently registered (%d):\n", len(reg.Repos))
		for _, name := range reg.ProjectNames() {
			fmt.Printf("  - %s → %s\n", name, reg.Repos[name].URL)
		}
		fmt.Println()
		if !w.confirm("Add more repos?") {
			return reg
		}
	}

	for {
		project := w.askEx("Linear project name (blank to finish)", askOpts{})
		if project == "" {
			return reg
		}
		url := w.askEx("  Git URL", askOpts{})
		if url == "" {
			fmt.Println("  Skipped — no URL given.")
			fmt.Println()
			continue
		}

		fmt.Printf("  Checking access to %s ... ", url)
		if err := checkRemoteAccess(url); err != nil {
			fmt.Println("FAILED")
			fmt.Println("  ⚠️  Could not reach that repo. The host running Nightshift needs git")
			fmt.Println("     auth for it — an SSH key, or 'gh auth login' for HTTPS URLs.")
			if !w.confirm("  Add it anyway?") {
				fmt.Println()
				continue
			}
		} else {
			fmt.Println("ok")
		}

		branch := w.askEx("  Main branch", askOpts{fallback: defaultMainBranch})
		reg.Repos[project] = config.RepoEntry{URL: url, MainBranch: branch}
		fmt.Printf("  ✅ Added %q\n\n", project)
	}
}

// ── Helpers (file I/O, validators, formatting) ──────────────────────────────

func pingLinear(apiKey string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("empty key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return linear.New(apiKey).Ping(ctx)
}

func testTelegram(botToken, chatID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return notify.New(true, botToken, chatID).SendSync(ctx,
		"🌙 *Nightshift setup* — this is a test message. If you can read this, your bot is configured correctly.")
}

func checkRemoteAccess(url string) error {
	return exec.Command("git", "ls-remote", "--exit-code", url, "HEAD").Run()
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func mask(s string) string {
	if s == "" {
		return "(unset)"
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func maskOrNone(s string) string {
	if s == "" {
		return "(disabled)"
	}
	return mask(s)
}

func yes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type envValues struct {
	linearKey, team, trigger, review             string
	mainBranch, repoPath                         string
	concurrency, dispatches, retries, timeoutMin string
	geminiKey                                    string
	tgEnabled, tgToken, tgChat, tgVerbose        string
	autoIterate, maxIter, prPoll, trusted        string
}

func writeEnvFile(path string, v envValues) error {
	// REPO_PATH is rendered as a comment when empty so users can see where
	// the fallback would live, mirroring the bash example.
	repoPathLine := `# REPO_PATH=""`
	if v.repoPath != "" {
		repoPathLine = fmt.Sprintf(`REPO_PATH="%s"`, v.repoPath)
	}

	body := fmt.Sprintf(`# Generated by ./nightshift setup on %s
# Re-run the wizard any time, or edit by hand.

LINEAR_API_KEY="%s"
LINEAR_TEAM_KEY="%s"
TRIGGER_STATE="%s"
IN_REVIEW_STATE="%s"

# Optional single-repo fallback for tickets whose project is not in repos.json
%s
MAIN_BRANCH="%s"

MAX_CONCURRENT="%s"
POLL_INTERVAL="30"
USE_AGENT_TEAMS="false"

MAX_DISPATCHES="%s"
MAX_RETRIES="%s"
AGENT_TIMEOUT_MINUTES="%s"

TELEGRAM_ENABLED="%s"
TELEGRAM_BOT_TOKEN="%s"
TELEGRAM_CHAT_ID="%s"
# Also notify on every ticket dispatch (more chatty)
TELEGRAM_VERBOSE="%s"

GEMINI_API_KEY="%s"
GEMINI_MODEL="gemini-2.5-pro"
MAX_REVIEW_RETRIES="1"

# Auto-iterate on PR review feedback (ENG-173). Opt-in. When true,
# Nightshift periodically polls open PRs it created for new review
# comments and re-engages Claude on the same branch.
AUTO_ITERATE_PRS="%s"
MAX_PR_ITERATIONS="%s"
PR_POLL_INTERVAL="%s"
# Comma-separated GitHub logins / bot names whose feedback Nightshift will
# act on. Humans are always trusted; empty = humans only (bots ignored).
TRUSTED_REVIEWERS="%s"
`,
		time.Now().Format(time.RFC3339),
		v.linearKey, v.team, v.trigger, v.review,
		repoPathLine, v.mainBranch,
		v.concurrency,
		v.dispatches, v.retries, v.timeoutMin,
		v.tgEnabled, v.tgToken, v.tgChat, v.tgVerbose,
		v.geminiKey,
		v.autoIterate, v.maxIter, v.prPoll, v.trusted,
	)
	return os.WriteFile(path, []byte(body), 0o600)
}

func writeReposFile(path string, reg *config.RepoRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
