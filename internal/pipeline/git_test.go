package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepoWithUpstream builds a temp repo with one commit on main and a
// refs/remotes/origin/main tracking ref pointing at it (no real remote needed),
// so branchAhead can be exercised against "origin/main".
func gitRepoWithUpstream(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main", "--quiet")
	run("config", "user.email", "t@t")
	run("config", "user.name", "T")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "c1", "--quiet")
	run("update-ref", "refs/remotes/origin/main", "HEAD") // fake upstream at HEAD
	return dir
}

func TestHasStagedChanges(t *testing.T) {
	dir := gitRepoWithUpstream(t)
	ctx := context.Background()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Clean index.
	if staged, err := hasStagedChanges(ctx, dir); err != nil || staged {
		t.Fatalf("clean: staged=%v err=%v", staged, err)
	}
	// New staged file.
	if err := os.WriteFile(filepath.Join(dir, "g.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	if staged, err := hasStagedChanges(ctx, dir); err != nil || !staged {
		t.Fatalf("staged: staged=%v err=%v", staged, err)
	}
	// After commit, clean again.
	git("commit", "-m", "c2", "--quiet")
	if staged, err := hasStagedChanges(ctx, dir); err != nil || staged {
		t.Fatalf("post-commit: staged=%v err=%v", staged, err)
	}
}

func TestBranchAhead(t *testing.T) {
	dir := gitRepoWithUpstream(t)
	ctx := context.Background()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// HEAD == origin/main → not ahead.
	if ahead, err := branchAhead(ctx, dir, "origin/main"); err != nil || ahead {
		t.Fatalf("level: ahead=%v err=%v", ahead, err)
	}
	// A new commit (the "Claude committed its own work" case) → ahead, even
	// though the working tree is clean.
	if err := os.WriteFile(filepath.Join(dir, "g.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "claude-commit", "--quiet")
	if ahead, err := branchAhead(ctx, dir, "origin/main"); err != nil || !ahead {
		t.Fatalf("ahead: ahead=%v err=%v", ahead, err)
	}
	if changed, _ := workingTreeChanged(ctx, dir); changed {
		t.Fatal("working tree should be clean after the commit (the bug scenario)")
	}
}

func TestBoundedReviewDiffTruncatesWithNotice(t *testing.T) {
	diff := strings.Repeat("a", maxReviewDiffBytes+1000)
	got := boundedReviewDiff(diff)
	if len(got) >= len(diff) {
		t.Fatalf("bounded diff length = %d, want less than original %d", len(got), len(diff))
	}
	if !strings.Contains(got, "diff truncated for review") {
		t.Fatalf("bounded diff missing truncation notice: %q", got)
	}
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "aaa") {
		t.Fatal("bounded diff should preserve both head and tail")
	}
}

func TestBoundedReviewDiffPreservesUTF8(t *testing.T) {
	diff := strings.Repeat("🙂", maxReviewDiffBytes/4+1000)
	got := boundedReviewDiff(diff)
	if !strings.Contains(got, "diff truncated for review") {
		t.Fatal("expected truncation notice")
	}
	if strings.ToValidUTF8(got, "") != got {
		t.Fatal("bounded diff should not split UTF-8 runes")
	}
}
