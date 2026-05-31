package repo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BranchName returns the branch Nightshift creates for a ticket
// (e.g. "nightshift/eng-42").
func BranchName(identifier string) string {
	return "nightshift/" + strings.ToLower(identifier)
}

// Worktree describes a freshly created git worktree.
type Worktree struct {
	Path   string
	Branch string
}

// CreateWorktree creates an isolated worktree at <base>/<identifier> on a new
// branch derived from origin/<mainBranch>. Any stale local branch or worktree
// at the same name is removed first so retries always start clean.
func CreateWorktree(ctx context.Context, base, identifier, repoPath, mainBranch string) (Worktree, error) {
	branch := BranchName(identifier)
	wt := filepath.Join(base, identifier)

	// Best-effort: pull the latest main and clear any stale state. Remove the
	// worktree BEFORE deleting the branch — git refuses to delete a branch
	// that's still checked out in a worktree, which would leave the branch
	// behind and make the `worktree add -b` below fail.
	_ = runIn(ctx, repoPath, "git", "fetch", "origin", mainBranch, "--quiet")
	_ = runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+mainBranch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s: %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

// ResumeWorktree creates a worktree from an EXISTING remote branch instead of
// starting fresh from main. Used when Nightshift re-engages on an open PR to
// address review comments or CI failures — prior commits stay intact and
// follow-up work appears as additional commits on the same branch.
//
// Fails (rather than falling back to main) if origin/<branch> doesn't exist;
// callers should use CreateWorktree for that case.
func ResumeWorktree(ctx context.Context, base, identifier, repoPath string) (Worktree, error) {
	branch := BranchName(identifier)
	wt := filepath.Join(base, identifier)

	if err := runIn(ctx, repoPath, "git", "fetch", "origin", branch, "--quiet"); err != nil {
		return Worktree{}, fmt.Errorf("git fetch origin %s: %w", branch, err)
	}

	// Clear any stale worktree + local branch so the resume starts from a
	// known-good remote tip. Worktree first: git won't delete a branch still
	// checked out in a worktree, and a leftover branch fails `worktree add -b`.
	_ = runIn(ctx, repoPath, "git", "worktree", "remove", "--force", wt)
	_ = runIn(ctx, repoPath, "git", "branch", "-D", branch)

	if err := runIn(ctx, repoPath, "git", "worktree", "add", "-b", branch, wt, "origin/"+branch); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add %s (resume): %w", wt, err)
	}
	return Worktree{Path: wt, Branch: branch}, nil
}

// CleanupWorktree removes the worktree for an identifier. We try the git
// worktree machinery first (which also clears the admin entry), and fall back
// to plain rm -rf if that fails — matching the bash predecessor.
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
