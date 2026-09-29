package repo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var repoLocks sync.Map

func lockRepo(repoPath string) func() {
	v, _ := repoLocks.LoadOrStore(filepath.Clean(repoPath), &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func BranchName(identifier string) string {
	return "noctra/" + strings.ToLower(identifier)
}

type Worktree struct {
	Path   string
	Branch string
}

func CreateWorktree(ctx context.Context, base, identifier, repoPath, mainBranch string) (Worktree, error) {
	branch := BranchName(identifier)
	wt := filepath.Join(base, identifier)

	defer lockRepo(repoPath)()

	_ = runIn(ctx, repoPath, "git", "fetch", "origin", mainBranch, "--quiet")
	clearWorktreePath(ctx, repoPath, wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+mainBranch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s: %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

func ResumeWorktree(ctx context.Context, base, identifier, repoPath string) (Worktree, error) {
	branch := BranchName(identifier)
	wt := filepath.Join(base, identifier)

	defer lockRepo(repoPath)()

	if err := runIn(ctx, repoPath, "git", "fetch", "origin", branch, "--quiet"); err != nil {
		return Worktree{}, fmt.Errorf("git fetch origin %s: %w", branch, err)
	}

	clearWorktreePath(ctx, repoPath, wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+branch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s (resume): %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

func CreateWorktreeWithBranch(ctx context.Context, base, identifier, repoPath, mainBranch, branch string) (Worktree, error) {
	wt := filepath.Join(base, identifier)

	defer lockRepo(repoPath)()

	_ = runIn(ctx, repoPath, "git", "fetch", "origin", mainBranch, "--quiet")
	clearWorktreePath(ctx, repoPath, wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+mainBranch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s: %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

func RemoteBranchExists(ctx context.Context, repoPath, branch string) bool {
	return runIn(ctx, repoPath, "git", "ls-remote", "--exit-code", "--heads", "origin", branch) == nil
}

func CreateOrResumeWorktree(ctx context.Context, base, identifier, repoPath, mainBranch string) (wt Worktree, resumed bool, err error) {
	if RemoteBranchExists(ctx, repoPath, BranchName(identifier)) {
		wt, err = ResumeWorktree(ctx, base, identifier, repoPath)
		if err == nil {
			return wt, true, nil
		}
		slog.Warn("repo: could not resume existing remote branch, starting fresh",
			"identifier", identifier, "branch", BranchName(identifier), "err", err)
	}
	wt, err = CreateWorktree(ctx, base, identifier, repoPath, mainBranch)
	return wt, false, err
}

func CleanupWorktree(ctx context.Context, repoPath, base, identifier string) {
	if identifier == "" {
		return
	}
	wt := filepath.Join(base, identifier)

	defer lockRepo(repoPath)()

	if err := runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt); err != nil {
		_ = os.RemoveAll(wt)
	}
}

func clearWorktreePath(ctx context.Context, repoPath, wt string) {
	if err := runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt); err == nil {
		return
	}
	if err := os.RemoveAll(wt); err != nil {
		slog.Warn("repo: could not remove leftover worktree directory", "path", wt, "err", err)
	}
	_ = runIn(ctx, repoPath, "git", "worktree", "prune")
}

func runIn(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
