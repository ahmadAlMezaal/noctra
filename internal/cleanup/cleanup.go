// Package cleanup implements `noctra cleanup` — pruning stale branches,
// worktrees, and old agent logs across every registered repo.
package cleanup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
)

// Run performs an interactive cleanup unless force is true.
func Run(ctx context.Context, cfg *config.Config, force bool) error {
	fmt.Println()
	fmt.Println("🧹 Noctra Cleanup")
	fmt.Println()

	resolver := repo.FromConfig(cfg)
	repos := resolver.AllRepoPaths()
	if len(repos) == 0 {
		return errors.New("no repos found — run ./noctra setup, or set REPO_PATH in .env")
	}

	in := bufio.NewScanner(os.Stdin)
	stats := struct {
		mergedDeleted   int
		unmergedDeleted int
		logsDeleted     int
	}{}

	for _, r := range repos {
		fmt.Printf("📁 %s\n", r)

		// ── Merged branches ─────────────────────────────────────────────────
		if branches := merged(ctx, r, cfg.MainBranch); len(branches) > 0 {
			fmt.Printf("  Merged branches to delete (%d):\n", len(branches))
			for _, b := range branches {
				fmt.Printf("    - %s\n", b)
			}
			if force || confirm(in, "  Delete these merged branches?") {
				for _, b := range branches {
					if err := runIn(ctx, r, "git", "branch", "-d", b); err == nil {
						stats.mergedDeleted++
					}
				}
			}
		}

		// ── Unmerged noctra/* branches ──────────────────────────────────
		if branches := unmergedNoctra(ctx, r, cfg.MainBranch); len(branches) > 0 {
			fmt.Println("  ⚠️  Unmerged Noctra branches (from failed runs):")
			for _, b := range branches {
				fmt.Printf("    - %s\n", b)
			}

			withPR, safe := partitionByOpenPR(ctx, r, branches)
			if len(withPR) > 0 {
				fmt.Println("    Skipping (have open PRs):")
				for _, b := range withPR {
					fmt.Printf("      - %s\n", b)
				}
			}
			if len(safe) > 0 {
				prompt := fmt.Sprintf("  Force-delete %d unmerged branch(es) without open PRs?", len(safe))
				if force || confirm(in, prompt) {
					for _, b := range safe {
						if err := runIn(ctx, r, "git", "branch", "-D", b); err == nil {
							stats.unmergedDeleted++
						}
					}
				}
			}
		}

		_ = runIn(ctx, r, "git", "worktree", "prune")
		_ = runIn(ctx, r, "git", "fetch", "--prune")
		fmt.Println()
	}

	// ── Stale worktrees ──────────────────────────────────────────────────────
	if dirs := worktreeDirs(cfg.WorktreeBase); len(dirs) > 0 {
		fmt.Printf("Stale worktrees (%d):\n", len(dirs))
		for _, d := range dirs {
			fmt.Printf("  - %s\n", filepath.Base(d))
		}
		if force || confirm(in, "Remove these worktrees?") {
			for _, d := range dirs {
				_ = os.RemoveAll(d)
			}
			for _, r := range repos {
				_ = runIn(ctx, r, "git", "worktree", "prune")
			}
		}
		fmt.Println()
	}

	// ── Old agent logs (>7 days) ─────────────────────────────────────────────
	if old := logsOlderThan(cfg.LogDir, 7*24*time.Hour); len(old) > 0 {
		fmt.Printf("Agent logs older than 7 days (%d):\n", len(old))
		for _, f := range old {
			fmt.Printf("  - %s\n", filepath.Base(f))
		}
		if force || confirm(in, "Delete these old log files?") {
			for _, f := range old {
				if err := os.Remove(f); err == nil {
					stats.logsDeleted++
				}
			}
		}
		fmt.Println()
	}

	fmt.Println("🧹 Cleanup complete:")
	fmt.Printf("  - Scanned %d repo(s)\n", len(repos))
	fmt.Printf("  - Deleted %d merged branch(es)\n", stats.mergedDeleted)
	fmt.Printf("  - Force-deleted %d unmerged branch(es)\n", stats.unmergedDeleted)
	fmt.Println("  - Pruned remote tracking refs")
	fmt.Printf("  - Cleared %d agent log(s) older than 7 days\n", stats.logsDeleted)
	fmt.Println()
	return nil
}

// merged returns local branches fully reachable from mainBranch, excluding
// the current and protected branches (main/master/staging).
func merged(ctx context.Context, repoPath, mainBranch string) []string {
	out, err := outputOf(ctx, repoPath, "git", "branch", "--merged", mainBranch)
	if err != nil {
		return nil
	}
	return filterBranches(out, func(name string) bool {
		switch name {
		case "main", "master", "staging":
			return false
		}
		return true
	})
}

// unmergedNoctra returns branches under noctra/* that have NOT been
// merged into mainBranch.
func unmergedNoctra(ctx context.Context, repoPath, mainBranch string) []string {
	out, err := outputOf(ctx, repoPath, "git", "branch", "--no-merged", mainBranch)
	if err != nil {
		return nil
	}
	return filterBranches(out, func(name string) bool {
		return strings.HasPrefix(name, "noctra/")
	})
}

func partitionByOpenPR(ctx context.Context, repoPath string, branches []string) (withPR, safe []string) {
	remote, _ := outputOf(ctx, repoPath, "git", "remote", "get-url", "origin")
	remote = strings.TrimSpace(remote)
	for _, b := range branches {
		hasPR := false
		if remote != "" {
			out, err := outputOf(ctx, repoPath, "gh", "pr", "list",
				"--repo", remote, "--head", b, "--state", "open", "--json", "number")
			if err == nil && strings.Contains(out, `"number"`) {
				hasPR = true
			}
		}
		if hasPR {
			withPR = append(withPR, b)
		} else {
			safe = append(safe, b)
		}
	}
	return withPR, safe
}

func filterBranches(raw string, keep func(string) bool) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "* ")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if keep(line) {
			out = append(out, line)
		}
	}
	return out
}

func worktreeDirs(base string) []string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(base, e.Name()))
		}
	}
	return out
}

func logsOlderThan(logDir string, age time.Duration) []string {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-age)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			out = append(out, filepath.Join(logDir, e.Name()))
		}
	}
	return out
}

func confirm(in *bufio.Scanner, prompt string) bool {
	fmt.Print(prompt, " [y/N] ")
	if !in.Scan() {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(in.Text()))
	return s == "y" || s == "yes"
}

func outputOf(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func runIn(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.Run()
}
