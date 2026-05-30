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

// TestResumeWorktree_PicksUpExistingBranchCommits creates a fresh worktree,
// commits a "marker" file to the branch, pushes it, then runs ResumeWorktree
// — the resumed worktree should carry the marker forward instead of being
// recreated from main.
func TestResumeWorktree_PicksUpExistingBranchCommits(t *testing.T) {
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
	mustGit("config", "commit.gpgsign", "false")
	mustGit("config", "receive.denyCurrentBranch", "ignore")
	mustGit("remote", "add", "origin", repo)

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("init"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-m", "init", "--quiet")
	mustGit("fetch", "origin", "--quiet")

	base := t.TempDir()
	ctx := context.Background()

	// Round 1 — fresh ticket: create worktree from main, add a commit on the
	// branch, push.
	wt1, err := CreateWorktree(ctx, base, "ENG-300", repo, "main")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	markerPath := filepath.Join(wt1.Path, "from-attempt-1.txt")
	if err := os.WriteFile(markerPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	runInWt := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wt1.Path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in worktree: %v\n%s", args, err, string(out))
		}
	}
	runInWt("add", "-A")
	runInWt("commit", "-m", "attempt-1", "--quiet")
	runInWt("push", "-u", "origin", wt1.Branch, "--quiet")

	// Tear it down between rounds — mirrors a real lifecycle where the
	// initial worktree is cleaned up after the first PR was created.
	CleanupWorktree(ctx, repo, base, "ENG-300")

	// Round 2 — resume: should bring back the marker, NOT start fresh.
	wt2, err := ResumeWorktree(ctx, base, "ENG-300", repo)
	if err != nil {
		t.Fatalf("ResumeWorktree: %v", err)
	}
	if wt2.Branch != "nightshift/eng-300" {
		t.Errorf("branch: got %q", wt2.Branch)
	}
	if _, err := os.Stat(filepath.Join(wt2.Path, "from-attempt-1.txt")); err != nil {
		t.Errorf("resumed worktree is missing the prior attempt's marker file: %v", err)
	}

	CleanupWorktree(ctx, repo, base, "ENG-300")
}

func TestResumeWorktree_FailsIfBranchNotOnRemote(t *testing.T) {
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
	mustGit("config", "commit.gpgsign", "false")
	mustGit("remote", "add", "origin", repo)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("init"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-m", "init", "--quiet")
	mustGit("fetch", "origin", "--quiet")

	if _, err := ResumeWorktree(context.Background(), t.TempDir(), "ENG-NEVER-PUSHED", repo); err == nil {
		t.Fatal("expected ResumeWorktree to fail when the branch isn't on origin")
	}
}
