package repo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BranchName returns the branch Noctra creates for a ticket (e.g. "noctra/eng-42").
func BranchName(identifier string) string {
	return "noctra/" + strings.ToLower(identifier)
}

// Worktree describes a freshly created git worktree.
type Worktree struct {
	Path   string
	Branch string
}

// CreateWorktree creates an isolated worktree at <base>/<identifier> on a new branch off origin/<mainBranch>, clearing any stale branch/worktree first so retries start clean.
func CreateWorktree(ctx context.Context, base, identifier, repoPath, mainBranch string) (Worktree, error) {
	branch := BranchName(identifier)
	wt := filepath.Join(base, identifier)

	// Remove the worktree BEFORE the branch — git won't delete a branch still checked out in a worktree, which would leave it behind and fail `worktree add -b`.
	_ = runIn(ctx, repoPath, "git", "fetch", "origin", mainBranch, "--quiet")
	_ = runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+mainBranch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s: %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

// ResumeWorktree creates a worktree from an EXISTING remote branch (not fresh from main) so re-engagement on an open PR keeps prior commits; fails if origin/<branch> is absent (use CreateWorktree then).
func ResumeWorktree(ctx context.Context, base, identifier, repoPath string) (Worktree, error) {
	branch := BranchName(identifier)
	wt := filepath.Join(base, identifier)

	if err := runIn(ctx, repoPath, "git", "fetch", "origin", branch, "--quiet"); err != nil {
		return Worktree{}, fmt.Errorf("git fetch origin %s: %w", branch, err)
	}

	// Worktree before branch: git won't delete a branch checked out in a worktree, and a leftover branch fails `worktree add -b`.
	_ = runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+branch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s (resume): %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

// CreateWorktreeWithBranch is CreateWorktree with an explicit branch name (not derived from the identifier), for sweep tasks using "noctra/sweep-<suffix>".
func CreateWorktreeWithBranch(ctx context.Context, base, identifier, repoPath, mainBranch, branch string) (Worktree, error) {
	wt := filepath.Join(base, identifier)

	_ = runIn(ctx, repoPath, "git", "fetch", "origin", mainBranch, "--quiet")
	_ = runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+mainBranch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s: %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

// CleanupWorktree removes an identifier's worktree via git (clears the admin entry too), falling back to rm -rf.
func CleanupWorktree(ctx context.Context, repoPath, base, identifier string) {
	if identifier == "" {
		return
	}
	wt := filepath.Join(base, identifier)
	if err := runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt); err != nil {
		_ = os.RemoveAll(wt)
	}
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
