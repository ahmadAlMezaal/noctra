package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCreateAndCleanupWorktree drives the real git binary against a temp
// repo, mirroring the bash-era test_worktree suite.
func TestCreateAndCleanupWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}

	mustGit("init", "-b", "main", "--quiet")
	mustGit("config", "user.email", "t@t")
	mustGit("config", "user.name", "T")
	// Throwaway fixture — keep commits hermetic regardless of host git config.
	mustGit("config", "commit.gpgsign", "false")
	mustGit("remote", "add", "origin", repo)

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("init"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-m", "init", "--quiet")
	mustGit("fetch", "origin", "--quiet")

	base := t.TempDir()
	ctx := context.Background()

	wt, err := CreateWorktree(ctx, base, "ENG-200", repo, "main")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if wt.Branch != "nightshift/eng-200" {
		t.Errorf("branch: got %q, want %q", wt.Branch, "nightshift/eng-200")
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}

	CleanupWorktree(ctx, repo, base, "ENG-200")
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("worktree dir should have been removed (err=%v)", err)
	}
}

func TestCreateWorktree_BadRepoPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, err := CreateWorktree(context.Background(), t.TempDir(), "ENG-999", "/does/not/exist", "main")
	if err == nil {
		t.Fatal("expected CreateWorktree to fail on a bad repo path")
	}
}
