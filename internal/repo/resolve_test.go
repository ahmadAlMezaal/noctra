package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
)

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
