// Package setup is the interactive wizard that generates .env and repos.json.
// It's the friendlier alternative to hand-editing the config files.
package setup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
)

// Run drives the wizard. It writes scriptDir/.env and scriptDir/repos.json.
func Run(scriptDir string) error {
	w := &wizard{in: bufio.NewScanner(os.Stdin)}

	fmt.Println()
	fmt.Println("🌙 Nightshift Setup")
	fmt.Println("   Generates .env and repos.json — press Enter to accept [defaults].")
	fmt.Println()

	envFile := filepath.Join(scriptDir, ".env")
	reposFile := filepath.Join(scriptDir, "repos.json")

	if _, err := os.Stat(envFile); err == nil {
		if !w.confirm(".env already exists — overwrite it?") {
			fmt.Println("Setup cancelled.")
			return nil
		}
		fmt.Println()
	}

	w.chooseTracker()
	fmt.Println()
	w.chooseEngine()
	fmt.Println()

	linearKey := w.askRequired("Linear API key", "The Linear API key is required.")
	team := w.ask("Linear team key", config.DefaultLinearTeamKey)
	trigger := w.ask("Trigger state", config.DefaultTriggerState)
	review := w.ask("In-review state", config.DefaultInReviewState)
	mainBranch := w.ask("Default main branch", config.DefaultMainBranch)
	concurrency := w.ask("Max concurrent tickets", fmt.Sprint(config.DefaultMaxConcurrent))
	fmt.Println()

	geminiKey := ""
	if w.confirm("Enable the Gemini review gate?") {
		geminiKey = w.ask("Gemini API key", "")
	}

	tgEnabled, tgToken, tgChat := "false", "", ""
	if w.confirm("Enable Telegram notifications?") {
		tgEnabled = "true"
		tgToken = w.ask("Telegram bot token", "")
		tgChat = w.ask("Telegram chat ID", "")
	}
	fmt.Println()

	reg := w.collectRepos(mainBranch)

	if err := writeEnvFile(envFile, envValues{
		linearKey:   linearKey,
		team:        team,
		trigger:     trigger,
		review:      review,
		mainBranch:  mainBranch,
		concurrency: concurrency,
		geminiKey:   geminiKey,
		tgEnabled:   tgEnabled,
		tgToken:     tgToken,
		tgChat:      tgChat,
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
	if count == 0 {
		fmt.Println("⚠️  No repos registered yet — add them to repos.json or re-run setup.")
	}
	fmt.Println()
	fmt.Println("Start Nightshift with: ./nightshift")
	return nil
}

type wizard struct {
	in *bufio.Scanner
}

func (w *wizard) readLine(prompt string) string {
	fmt.Print(prompt)
	if !w.in.Scan() {
		return ""
	}
	return strings.TrimSpace(w.in.Text())
}

func (w *wizard) ask(label, def string) string {
	var prompt string
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", label, def)
	} else {
		prompt = label + ": "
	}
	if s := w.readLine(prompt); s != "" {
		return s
	}
	return def
}

func (w *wizard) askRequired(label, missingMsg string) string {
	for {
		if s := w.ask(label, ""); s != "" {
			return s
		}
		fmt.Println("  " + missingMsg)
	}
}

func (w *wizard) confirm(prompt string) bool {
	s := strings.ToLower(w.readLine(prompt + " [y/N] "))
	return s == "y" || s == "yes"
}

func (w *wizard) chooseTracker() {
	fmt.Println("Issue tracker:")
	fmt.Println("  1) Linear")
	fmt.Println("  2) Jira             (coming soon)")
	fmt.Println("  3) GitHub Issues    (coming soon)")
	for {
		switch w.ask("Choose", "1") {
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
		switch w.ask("Choose", "1") {
		case "1":
			return
		case "2":
			fmt.Println("  ⏳ Gemini as an engine isn't supported yet — Claude Code only for now.")
		default:
			fmt.Println("  Enter 1 or 2.")
		}
	}
}

func (w *wizard) collectRepos(defaultMainBranch string) *config.RepoRegistry {
	fmt.Println("Register repos — map each Linear project to a git repo.")
	fmt.Println("Nightshift clones these on demand; nothing needs to be cloned yet.")
	fmt.Println()

	reg := &config.RepoRegistry{Repos: map[string]config.RepoEntry{}}
	for {
		project := w.ask("Linear project name (blank to finish)", "")
		if project == "" {
			return reg
		}
		url := w.ask("  Git URL", "")
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

		branch := w.ask("  Main branch", defaultMainBranch)
		reg.Repos[project] = config.RepoEntry{URL: url, MainBranch: branch}
		fmt.Printf("  ✅ Added %q\n\n", project)
	}
}

func checkRemoteAccess(url string) error {
	cmd := exec.Command("git", "ls-remote", "--exit-code", url, "HEAD")
	return cmd.Run()
}

type envValues struct {
	linearKey, team, trigger, review, mainBranch, concurrency string
	geminiKey, tgEnabled, tgToken, tgChat                     string
}

func writeEnvFile(path string, v envValues) error {
	body := fmt.Sprintf(`# Generated by ./nightshift setup on %s
# Re-run the wizard any time, or edit by hand.

LINEAR_API_KEY="%s"
LINEAR_TEAM_KEY="%s"
TRIGGER_STATE="%s"
IN_REVIEW_STATE="%s"

# Optional single-repo fallback for tickets whose project is not in repos.json
# REPO_PATH=""
MAIN_BRANCH="%s"

MAX_CONCURRENT="%s"
POLL_INTERVAL="30"
USE_AGENT_TEAMS="false"

MAX_DISPATCHES="10"
MAX_RETRIES="3"
AGENT_TIMEOUT_MINUTES="45"

TELEGRAM_ENABLED="%s"
TELEGRAM_BOT_TOKEN="%s"
TELEGRAM_CHAT_ID="%s"

GEMINI_API_KEY="%s"
GEMINI_MODEL="gemini-2.5-pro"
MAX_REVIEW_RETRIES="1"
`,
		time.Now().Format(time.RFC3339),
		v.linearKey, v.team, v.trigger, v.review,
		v.mainBranch, v.concurrency,
		v.tgEnabled, v.tgToken, v.tgChat, v.geminiKey)
	return os.WriteFile(path, []byte(body), 0o600)
}

func writeReposFile(path string, reg *config.RepoRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
