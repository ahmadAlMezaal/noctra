package repo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestEnsureCloned_ConcurrentProducesCompleteRepo: two concurrent clones of one dest yield a complete repo (origin/main resolvable) — nobody sees it mid-clone. Cold-clone race regression (ENG-180).
func TestEnsureCloned_ConcurrentProducesCompleteRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Source repo with a commit on main.
	src := t.TempDir()
	git(src, "init", "-b", "main", "--quiet")
	git(src, "config", "user.email", "t@t")
	git(src, "config", "user.name", "T")
	git(src, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(src, "add", "-A")
	git(src, "commit", "-m", "init", "--quiet")

	dest := filepath.Join(t.TempDir(), "repo")
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = ensureCloned(ctx, src, dest) }(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("ensureCloned[%d]: %v", i, err)
		}
	}
	if !isGitRepo(dest) {
		t.Fatal("dest is not a git repo after clone")
	}
	// Clone must be complete: origin/main resolves (a mid-clone repo would fail here — the bug).
	rp := exec.Command("git", "rev-parse", "--verify", "origin/main")
	rp.Dir = dest
	if out, err := rp.CombinedOutput(); err != nil {
		t.Fatalf("origin/main not resolvable in cloned repo: %v\n%s", err, out)
	}
	// No leftover temp clone dirs beside dest.
	entries, _ := filepath.Glob(dest + ".cloning-*")
	if len(entries) != 0 {
		t.Errorf("leftover temp clone dirs: %v", entries)
	}
}

// fakeGitRepo makes a dir with a .git/ subdir so isGitRepo reports true without shelling out.
func fakeGitRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestResolve_FallbackToRepoPath(t *testing.T) {
	fallback := fakeGitRepo(t)

	r := &Resolver{
		ReposBase:  t.TempDir(),
		RepoPath:   fallback,
		MainBranch: "main",
	}

	res, err := r.Resolve(context.Background(), "Project Without Directive")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Path != fallback {
		t.Errorf("Path: got %q, want %q", res.Path, fallback)
	}
	if res.MainBranch != "main" {
		t.Errorf("MainBranch: got %q", res.MainBranch)
	}
}

func TestResolve_NoDirectiveAndNoFallback(t *testing.T) {
	r := &Resolver{
		ReposBase:  t.TempDir(),
		MainBranch: "main",
	}
	_, err := r.Resolve(context.Background(), "Some Project")
	if err == nil || !strings.Contains(err.Error(), "no `Repo:` directive") {
		t.Fatalf("expected no-directive error, got %v", err)
	}
	_, err = r.Resolve(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no Linear project") {
		t.Fatalf("expected empty-project error, got %v", err)
	}
}

func TestResolve_NoDirectiveReturnsNonTransient(t *testing.T) {
	r := &Resolver{
		ReposBase:  t.TempDir(),
		MainBranch: "main",
	}

	// Project with no directive, no fallback → NonTransientError.
	_, err := r.Resolve(context.Background(), "Missing Project")
	if err == nil {
		t.Fatal("expected error for project without a directive")
	}
	var nte *NonTransientError
	if !errors.As(err, &nte) {
		t.Fatalf("expected NonTransientError, got %T: %v", err, err)
	}
	if !strings.Contains(nte.Error(), "no `Repo:` directive") {
		t.Errorf("unexpected message: %q", nte.Error())
	}

	// Empty project, no fallback → NonTransientError.
	_, err = r.Resolve(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty project")
	}
	if !errors.As(err, &nte) {
		t.Fatalf("expected NonTransientError, got %T: %v", err, err)
	}
	if !strings.Contains(nte.Error(), "no Linear project") {
		t.Errorf("unexpected message: %q", nte.Error())
	}
}

func TestResolve_FallbackToRepoPathIsNotNonTransient(t *testing.T) {
	fallback := fakeGitRepo(t)
	r := &Resolver{
		ReposBase:  t.TempDir(),
		RepoPath:   fallback,
		MainBranch: "main",
	}

	// Project without a directive but WITH a valid fallback → success.
	res, err := r.Resolve(context.Background(), "Project Without Directive")
	if err != nil {
		t.Fatalf("expected success with fallback, got %v", err)
	}
	if res.Path != fallback {
		t.Errorf("Path: got %q, want %q", res.Path, fallback)
	}
}

func TestAllRepoPaths_ScansReposBaseAndFilters(t *testing.T) {
	base := t.TempDir()

	// A cloned repo under ReposBase, plus a non-git dir that must be skipped.
	cloned := filepath.Join(base, "owner-web-app")
	if err := os.MkdirAll(filepath.Join(cloned, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	fallback := fakeGitRepo(t)

	r := &Resolver{ReposBase: base, RepoPath: fallback}

	paths := r.AllRepoPaths()
	if len(paths) != 2 {
		t.Fatalf("paths: got %v", paths)
	}
	if paths[0] != cloned {
		t.Errorf("first path: got %q, want %q", paths[0], cloned)
	}
	if paths[1] != fallback {
		t.Errorf("second path: got %q, want %q", paths[1], fallback)
	}
}

func TestResolveDirect_AlreadyClonedUsesExplicitBranch(t *testing.T) {
	base := t.TempDir()
	// Pre-create the slug dir as a "clone" so ResolveDirect skips the network.
	dest := filepath.Join(base, Slug("owner/site-repo"))
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{ReposBase: base, MainBranch: "main"}

	res, err := r.ResolveDirect(context.Background(), "owner/site-repo", "staging")
	if err != nil {
		t.Fatalf("ResolveDirect: %v", err)
	}
	if res.Path != dest {
		t.Errorf("Path: got %q, want %q", res.Path, dest)
	}
	if res.MainBranch != "staging" {
		t.Errorf("explicit branch should win: got %q", res.MainBranch)
	}
}

func TestResolveDirect_NoBranchFallsBackToMainBranch(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, Slug("owner/site-repo"))
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Resolver{ReposBase: base, MainBranch: "main"}

	// No branch given and the fake repo has no origin/HEAD → fall back.
	res, err := r.ResolveDirect(context.Background(), "owner/site-repo", "")
	if err != nil {
		t.Fatalf("ResolveDirect: %v", err)
	}
	if res.MainBranch != "main" {
		t.Errorf("branch fallback: got %q, want main", res.MainBranch)
	}
}

func TestResolveDirect_InvalidRefIsNonTransient(t *testing.T) {
	r := &Resolver{ReposBase: t.TempDir(), MainBranch: "main"}
	_, err := r.ResolveDirect(context.Background(), "this is not a repo!", "")
	if err == nil {
		t.Fatal("expected an error for an invalid ref")
	}
	var nte *NonTransientError
	if !errors.As(err, &nte) {
		t.Fatalf("want NonTransientError, got %T: %v", err, err)
	}
}
