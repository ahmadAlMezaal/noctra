package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func gitMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(msg), 0o600); err != nil {
		t.Fatal(err)
	}
	gitMust(t, dir, "add", "-A")
	gitMust(t, dir, "commit", "-m", msg, "--quiet")
}

func cloneWithBareOrigin(t *testing.T) (clone, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin = t.TempDir()
	gitMust(t, origin, "init", "--bare", "-b", "main", "--quiet")

	clone = t.TempDir()
	gitMust(t, clone, "init", "-b", "main", "--quiet")
	gitMust(t, clone, "config", "user.email", "t@t")
	gitMust(t, clone, "config", "user.name", "T")
	gitMust(t, clone, "config", "commit.gpgsign", "false")
	gitMust(t, clone, "remote", "add", "origin", origin)
	commitFile(t, clone, "README.md", "init")
	gitMust(t, clone, "push", "-u", "origin", "main", "--quiet")
	return clone, origin
}

func TestSweepPushArgs(t *testing.T) {
	branch := "noctra/sweep-repo-deps-update"
	if got, want := sweepPushArgs(branch, ""), []string{"push", "-u", "origin", branch}; !reflect.DeepEqual(got, want) {
		t.Errorf("no remote branch: got %v, want %v", got, want)
	}
	want := []string{"push", "-u", "--force-with-lease=refs/heads/" + branch + ":abc123", "origin", branch}
	if got := sweepPushArgs(branch, "abc123"); !reflect.DeepEqual(got, want) {
		t.Errorf("stale remote branch: got %v, want %v", got, want)
	}
}

func TestPushSweepBranchReplacesStaleRemoteBranch(t *testing.T) {
	clone, origin := cloneWithBareOrigin(t)
	ctx := context.Background()
	branch := "noctra/sweep-repo-deps-update"

	gitMust(t, clone, "checkout", "-q", "-b", branch)
	commitFile(t, clone, "old.txt", "previous sweep, already merged")
	gitMust(t, clone, "push", "-q", "origin", branch)

	gitMust(t, clone, "checkout", "-q", "main")
	commitFile(t, clone, "main.txt", "main moved on")
	gitMust(t, clone, "push", "-q", "origin", "main")
	gitMust(t, clone, "branch", "-q", "-D", branch)
	gitMust(t, clone, "checkout", "-q", "-b", branch, "main")
	commitFile(t, clone, "new.txt", "fresh sweep")

	if err := runIn(ctx, clone, "git", "push", "-u", "origin", branch); err == nil {
		t.Fatal("expected a plain push over the stale branch to be rejected")
	}
	if err := pushSweepBranch(ctx, clone, branch); err != nil {
		t.Fatalf("pushSweepBranch: %v", err)
	}
	if got, want := gitMust(t, origin, "rev-parse", branch), gitMust(t, clone, "rev-parse", "HEAD"); got != want {
		t.Errorf("remote branch = %s, want local HEAD %s", got, want)
	}
}

func TestPushSweepBranchCreatesMissingRemoteBranch(t *testing.T) {
	clone, origin := cloneWithBareOrigin(t)
	branch := "noctra/sweep-repo-lint-cleanup"

	gitMust(t, clone, "checkout", "-q", "-b", branch)
	commitFile(t, clone, "lint.txt", "lint fixes")

	if err := pushSweepBranch(context.Background(), clone, branch); err != nil {
		t.Fatalf("pushSweepBranch: %v", err)
	}
	if got, want := gitMust(t, origin, "rev-parse", branch), gitMust(t, clone, "rev-parse", "HEAD"); got != want {
		t.Errorf("remote branch = %s, want local HEAD %s", got, want)
	}
}
