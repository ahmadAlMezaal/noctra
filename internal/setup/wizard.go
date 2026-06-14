// Package setup is the interactive wizard that generates .env. It's the
// friendlier alternative to hand-editing the config file. Repos are routed
// per-project from the Linear project's `Repo: owner/name` directive.
//
// On re-run, every prompt is pre-filled with the value currently in .env (or
// the static default if absent). Press Enter to keep, type to replace. The
// wizard also offers a "manual mode" that just copies the example template
// into place for users who prefer to edit by hand.
package setup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
)

// Run drives the wizard. It writes scriptDir/.env.
func Run(scriptDir string) error {
	envFile := filepath.Join(scriptDir, ".env")

	existingEnv, _ := config.LoadEnvFile(envFile)

	w := &wizard{in: bufio.NewScanner(os.Stdin)}

	fmt.Println()
	fmt.Println("🌙 Noctra Setup")
	fmt.Println("   Generates .env — press Enter to accept [defaults].")
	fmt.Println("   Repos are declared per-project in Linear (a `Repo: owner/name` line in")
	fmt.Println("   the project description).")
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
	agentBackend := w.chooseEngine(existingEnv["AGENT_BACKEND"])
	fmt.Println()

	w.printCLIStatus(agentBackend)
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

	// Trigger mode: state (column) or label.
	triggerMode := w.chooseTriggerMode(existingEnv["TRIGGER_MODE"])
	trigger := ""
	triggerLabel := ""
	if triggerMode == "label" {
		triggerLabel = w.askEx("Trigger label name", askOpts{
			existing: existingEnv["TRIGGER_LABEL"],
			required: true,
		})
	} else {
		trigger = w.askEx("Trigger state", askOpts{
			existing: existingEnv["TRIGGER_STATE"],
			fallback: config.DefaultTriggerState,
		})
	}

	review := w.askEx("In-review state", askOpts{
		existing: existingEnv["IN_REVIEW_STATE"],
		fallback: config.DefaultInReviewState,
	})
	fmt.Println()

	// ── Repos: directive-first routing ───────────────────────────────────────────
	mainBranch := w.askEx("Default main branch", askOpts{
		existing: existingEnv["MAIN_BRANCH"],
		fallback: config.DefaultMainBranch,
	})

	// Repos are routed per-ticket from each Linear project's description.
	fmt.Println()
	fmt.Println("─── Repos ───")
	fmt.Println("Repos are declared per-project in Linear. Add this to each project's")
	fmt.Println("description so Noctra knows where to open PRs:")
	fmt.Println("  Repo: your-org/your-repo")
	fmt.Println("  Branch: main   (optional — defaults to the repo's default branch)")

	// ── Optional REPO_PATH fallback ────────────────────────────────────────────
	fmt.Println()
	fmt.Println("─── Single-repo fallback (optional) ───")
	fmt.Println("Used only for tickets whose Linear project has no `Repo:` directive.")
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
	geminiMode := existingEnv["GEMINI_MODE"]
	if geminiMode == "" {
		geminiMode = config.DefaultGeminiMode
	}
	if strings.EqualFold(geminiMode, "cli") || existingEnv["GEMINI_API_KEY"] != "" {
		fmt.Println("Gemini review gate is currently enabled.")
		if w.confirm("Keep it enabled?") {
			geminiMode = w.chooseGeminiMode(geminiMode)
			if geminiMode == "api" {
				geminiKey = w.askEx("Gemini API key", askOpts{existing: existingEnv["GEMINI_API_KEY"], secret: true})
			}
		} else {
			geminiMode = config.DefaultGeminiMode
		}
	} else if w.confirm("Enable the Gemini review gate?") {
		geminiMode = w.chooseGeminiMode(geminiMode)
		if geminiMode == "api" {
			geminiKey = w.askEx("Gemini API key", askOpts{secret: true})
		}
	} else {
		geminiMode = config.DefaultGeminiMode
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
	fmt.Printf("  AGENT_BACKEND         = %s\n", agentBackend)
	fmt.Printf("  TRIGGER_MODE          = %s\n", triggerMode)
	if triggerMode == "label" {
		fmt.Printf("  TRIGGER_LABEL         = %s\n", triggerLabel)
	} else {
		fmt.Printf("  TRIGGER_STATE         = %s\n", trigger)
	}
	fmt.Printf("  IN_REVIEW_STATE       = %s\n", review)
	fmt.Printf("  MAIN_BRANCH           = %s\n", mainBranch)
	if repoPath != "" {
		fmt.Printf("  REPO_PATH             = %s\n", repoPath)
	}
	fmt.Printf("  MAX_CONCURRENT        = %d\n", concurrency)
	fmt.Printf("  MAX_DISPATCHES        = %d\n", dispatches)
	fmt.Printf("  MAX_RETRIES           = %d\n", retries)
	fmt.Printf("  AGENT_TIMEOUT_MINUTES = %d\n", timeoutMin)
	fmt.Printf("  GEMINI_MODE           = %s\n", geminiMode)
	if geminiMode == "cli" {
		fmt.Printf("  GEMINI_API_KEY        = (not used in cli mode)\n")
	} else {
		fmt.Printf("  GEMINI_API_KEY        = %s\n", maskOrNone(geminiKey))
	}
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
	if !w.confirm("Save to .env?") {
		fmt.Println("Setup cancelled — no files changed.")
		return nil
	}

	// ── Write files ────────────────────────────────────────────────────────────
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return fmt.Errorf("create config dir %s: %w", scriptDir, err)
	}
	if err := writeEnvFile(envFile, envValues{
		linearKey:    linearKey,
		team:         team,
		agentBackend: agentBackend,
		triggerMode:  triggerMode,
		trigger:      trigger,
		triggerLabel: triggerLabel,
		review:       review,
		mainBranch:   mainBranch,
		repoPath:     repoPath,
		concurrency:  strconv.Itoa(concurrency),
		dispatches:   strconv.Itoa(dispatches),
		retries:      strconv.Itoa(retries),
		timeoutMin:   strconv.Itoa(timeoutMin),
		geminiKey:    geminiKey,
		geminiMode:   geminiMode,
		tgEnabled:    tgEnabled,
		tgToken:      tgToken,
		tgChat:       tgChat,
		tgVerbose:    tgVerbose,
		autoIterate:  autoIterate,
		maxIter:      strconv.Itoa(maxIter),
		prPoll:       strconv.Itoa(prPoll),
		trusted:      trusted,
	}); err != nil {
		return fmt.Errorf("write %s: %w", envFile, err)
	}
	fmt.Println()
	fmt.Printf("✅ Wrote %s\n", envFile)
	fmt.Println("ℹ️  Repos are routed via each Linear project's `Repo: owner/name`")
	fmt.Println("   directive. Add it to your project descriptions.")
	fmt.Println()
	fmt.Println("Start Noctra with: ./noctra")
	return nil
}

// runManual copies .env.example → .env, asking before overwriting it. The
// caller passes its own scanner so we share the same input stream —
// constructing a second bufio.Scanner on os.Stdin would risk losing bytes the
// first scanner already buffered.
//
// Repos are not scaffolded from a template here: tickets are routed to their
// repo by the Linear project's `Repo:` directive (a `Repo: owner/name` line in
// the project description).
func runManual(scriptDir string, in *bufio.Scanner) error {
	src := filepath.Join(scriptDir, ".env.example")
	dst := filepath.Join(scriptDir, ".env")
	if _, err := os.Stat(src); err != nil {
		// Manual setup's only job is copying this template — if it's missing
		// (e.g. run outside a checkout), there's nothing useful to do, so fail
		// loudly rather than exit 0. Use interactive setup instead.
		return fmt.Errorf("template not found: %s (run `noctra setup` interactively instead): %w", src, err)
	}

	create := true
	if _, err := os.Stat(dst); err == nil {
		fmt.Print(filepath.Base(dst), " already exists — overwrite? [y/N] ")
		if !in.Scan() || !yes(in.Text()) {
			fmt.Println("   kept existing")
			create = false
		}
	}
	if create {
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s → %s: %w", src, dst, err)
		}
		fmt.Printf("📄 Created %s\n", dst)
	}
	fmt.Println()
	fmt.Println("Edit .env with your values, then run: ./noctra")
	fmt.Println()
	fmt.Println("Repos are routed per-ticket from the Linear project's description:")
	fmt.Println("  Repo: your-org/your-repo")
	fmt.Println("  Branch: main   (optional — defaults to the repo's default branch)")
	fmt.Println("Full https:// / git@ URLs work too (for SSH or non-GitHub hosts).")
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
	fmt.Println("How would you like to configure Noctra?")
	fmt.Println("  1) Interactive setup (guided prompts) — recommended")
	fmt.Println("  2) Manual setup (copies .env.example — you fill it in)")
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

func (w *wizard) chooseTriggerMode(existing string) string {
	fmt.Println("How should Noctra pick up tickets?")
	fmt.Println("  1) State — watch a specific Linear column (e.g. \"Next\")")
	fmt.Println("  2) Label — watch for a label (e.g. \"noctra\")")
	fallback := "1"
	if strings.EqualFold(existing, "label") {
		fallback = "2"
	}
	for {
		s := w.askEx("Choose", askOpts{fallback: fallback})
		if w.eof {
			if existing == "label" {
				return "label"
			}
			return "state"
		}
		switch s {
		case "1":
			return "state"
		case "2":
			return "label"
		default:
			fmt.Println("  Enter 1 or 2.")
		}
	}
}

// chooseEngine asks which coding-agent backend to dispatch tickets with and
// returns the canonical backend name ("claude" / "codex") for AGENT_BACKEND.
func (w *wizard) chooseEngine(existing string) string {
	fmt.Println("Coding-agent engine:")
	fmt.Println("  1) Claude Code      (claude CLI)")
	fmt.Println("  2) OpenAI Codex     (codex CLI — run `codex login` once on the host)")
	fallback := "1"
	if strings.EqualFold(existing, "codex") {
		fallback = "2"
	}
	for {
		s := w.askEx("Choose", askOpts{fallback: fallback})
		if w.eof {
			if strings.EqualFold(existing, "codex") {
				return "codex"
			}
			return "claude"
		}
		switch s {
		case "1":
			return "claude"
		case "2":
			return "codex"
		default:
			fmt.Println("  Enter 1 or 2.")
		}
	}
}

func (w *wizard) chooseGeminiMode(existing string) string {
	fmt.Println("Gemini review mode:")
	fmt.Println("  1) API — uses GEMINI_API_KEY from Google AI Studio")
	fmt.Println("  2) CLI — uses the gemini CLI; run `gemini` once on the host to log in")
	fallback := "1"
	if strings.EqualFold(existing, "cli") {
		fallback = "2"
	}
	for {
		s := w.askEx("Choose", askOpts{fallback: fallback})
		if w.eof {
			if strings.EqualFold(existing, "cli") {
				return "cli"
			}
			return "api"
		}
		switch s {
		case "1":
			return "api"
		case "2":
			return "cli"
		default:
			fmt.Println("  Enter 1 or 2.")
		}
	}
}

func (w *wizard) printCLIStatus(agentBackend string) {
	fmt.Println("Required CLIs:")
	clis := []string{"git", "gh", config.AgentCLIs()[agentBackend]}
	for _, cmd := range clis {
		if cmd == "" {
			continue
		}
		if _, err := exec.LookPath(cmd); err == nil {
			fmt.Printf("  ✅ %s\n", cmd)
		} else {
			fmt.Printf("  ⚠️  %s — not found in PATH (install before running ./noctra)\n", cmd)
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
		"🌙 *Noctra setup* — this is a test message. If you can read this, your bot is configured correctly.")
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
	linearKey, team                              string
	agentBackend                                 string
	triggerMode, trigger, triggerLabel, review   string
	mainBranch, repoPath                         string
	concurrency, dispatches, retries, timeoutMin string
	geminiMode, geminiKey                        string
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

	// Render trigger lines based on mode.
	triggerLines := fmt.Sprintf("TRIGGER_MODE=\"%s\"\n", v.triggerMode)
	if v.triggerMode == "label" {
		triggerLines += fmt.Sprintf("TRIGGER_LABEL=\"%s\"\n", v.triggerLabel)
		if v.trigger != "" {
			triggerLines += fmt.Sprintf("# TRIGGER_STATE=\"%s\"\n", v.trigger)
		}
	} else {
		triggerLines += fmt.Sprintf("TRIGGER_STATE=\"%s\"\n", v.trigger)
	}

	body := fmt.Sprintf(`# Generated by ./noctra setup on %s
# Re-run the wizard any time, or edit by hand.

LINEAR_API_KEY="%s"
LINEAR_TEAM_KEY="%s"
%sIN_REVIEW_STATE="%s"

# Optional single-repo fallback for tickets whose project has no Repo: directive
%s
MAIN_BRANCH="%s"

# Coding-agent backend: "claude" (default) or "codex".
# codex requires the OpenAI Codex CLI on PATH + a one-time 'codex login'.
AGENT_BACKEND="%s"

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

# Gemini review gate: "api" uses GEMINI_API_KEY; "cli" shells out to gemini.
# For cli mode, install Gemini CLI and run 'gemini' once on this host to log in.
GEMINI_MODE="%s"
GEMINI_API_KEY="%s"
GEMINI_MODEL="gemini-2.5-pro"
MAX_REVIEW_RETRIES="1"

# Auto-iterate on PR review feedback (ENG-173). Opt-in. When true,
# Noctra periodically polls open PRs it created for new review
# comments and re-engages Claude on the same branch.
AUTO_ITERATE_PRS="%s"
MAX_PR_ITERATIONS="%s"
PR_POLL_INTERVAL="%s"
# Comma-separated GitHub logins / bot names whose feedback Noctra will
# act on. Humans are always trusted; empty = humans only (bots ignored).
TRUSTED_REVIEWERS="%s"
`,
		time.Now().Format(time.RFC3339),
		v.linearKey, v.team, triggerLines, v.review,
		repoPathLine, v.mainBranch,
		v.agentBackend,
		v.concurrency,
		v.dispatches, v.retries, v.timeoutMin,
		v.tgEnabled, v.tgToken, v.tgChat, v.tgVerbose,
		v.geminiMode, v.geminiKey,
		v.autoIterate, v.maxIter, v.prPoll, v.trusted,
	)
	return os.WriteFile(path, []byte(body), 0o600)
}
