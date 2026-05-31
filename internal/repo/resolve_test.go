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

	"github.com/ahmadAlMezaal/nightshift/internal/config"
)

// TestEnsureCloned_ConcurrentProducesCompleteRepo drives two concurrent
// ensureCloned calls at the same un-cloned dest and asserts the result is a
// complete clone (origin/main resolvable) — i.e. nobody observes the repo
// mid-clone. Regression test for the cold-clone race (ENG-180).
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
	// The clone must be complete: origin/main must resolve. (A repo observed
	// mid-clone would fail here — exactly the bug.)
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

// fakeGitRepo creates a directory containing a .git/ subdir so isGitRepo
// reports true without us shelling out to git.
func fakeGitRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestResolve_RegistryHitForAlreadyClonedRepo(t *testing.T) {
	base := t.TempDir()
	// Pre-create the slug dir as a "clone" — Resolve should skip the clone.
	clonedPath := filepath.Join(base, "auth-service")
	if err := os.MkdirAll(filepath.Join(clonedPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	reg := &config.RepoRegistry{
		Repos: map[string]config.RepoEntry{
			"Auth Service": {URL: "git@example.com:me/auth.git", MainBranch: "develop"},
		},
	}
	r := &Resolver{Registry: reg, ReposBase: base, MainBranch: "main"}

	res, err := r.Resolve(context.Background(), "Auth Service")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Path != clonedPath {
		t.Errorf("Path: got %q, want %q", res.Path, clonedPath)
	}
	if res.MainBranch != "develop" {
		t.Errorf("MainBranch: got %q, want %q", res.MainBranch, "develop")
	}
}

func TestResolve_FallbackToRepoPath(t *testing.T) {
	fallback := fakeGitRepo(t)

	r := &Resolver{
		Registry:   &config.RepoRegistry{Repos: map[string]config.RepoEntry{}},
		ReposBase:  t.TempDir(),
		RepoPath:   fallback,
		MainBranch: "main",
	}

	res, err := r.Resolve(context.Background(), "Unmapped Project")
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

func TestResolve_NoMatchAndNoFallback(t *testing.T) {
	r := &Resolver{
		Registry:   &config.RepoRegistry{Repos: map[string]config.RepoEntry{}},
		ReposBase:  t.TempDir(),
		MainBranch: "main",
	}
	_, err := r.Resolve(context.Background(), "Unmapped")
	if err == nil || !strings.Contains(err.Error(), "no repo is mapped") {
		t.Fatalf("expected mapped-error, got %v", err)
	}
	_, err = r.Resolve(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no Linear project") {
		t.Fatalf("expected empty-project error, got %v", err)
	}
}

func TestResolve_NoMatchReturnsNonTransient(t *testing.T) {
	r := &Resolver{
		Registry:   &config.RepoRegistry{Repos: map[string]config.RepoEntry{}},
		ReposBase:  t.TempDir(),
		MainBranch: "main",
	}

	// Unmapped project, no fallback → NonTransientError.
	_, err := r.Resolve(context.Background(), "Missing Project")
	if err == nil {
		t.Fatal("expected error for unmapped project")
	}
	var nte *NonTransientError
	if !errors.As(err, &nte) {
		t.Fatalf("expected NonTransientError, got %T: %v", err, err)
	}
	if !strings.Contains(nte.Error(), "no repo is mapped") {
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
		Registry:   &config.RepoRegistry{Repos: map[string]config.RepoEntry{}},
		ReposBase:  t.TempDir(),
		RepoPath:   fallback,
		MainBranch: "main",
	}

	// Unmapped project WITH a valid fallback → success, no error at all.
	res, err := r.Resolve(context.Background(), "Unmapped Project")
	if err != nil {
		t.Fatalf("expected success with fallback, got %v", err)
	}
	if res.Path != fallback {
		t.Errorf("Path: got %q, want %q", res.Path, fallback)
	}
}

func TestAllRepoPaths_DedupesAndFilters(t *testing.T) {
	base := t.TempDir()

	// Two registered projects; only one is actually cloned.
	cloned := filepath.Join(base, "web-app")
	if err := os.MkdirAll(filepath.Join(cloned, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	reg := &config.RepoRegistry{
		Repos: map[string]config.RepoEntry{
			"Web App":      {URL: "u"},
			"Not Yet Used": {URL: "u2"},
		},
	}
	fallback := fakeGitRepo(t)

	r := &Resolver{Registry: reg, ReposBase: base, RepoPath: fallback}

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
