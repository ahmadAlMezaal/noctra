package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/github"
)

type NonTransientError struct {
	Err error
}

func (e *NonTransientError) Error() string { return e.Err.Error() }
func (e *NonTransientError) Unwrap() error { return e.Err }

type Resolved struct {
	Path       string
	MainBranch string
}

type Resolver struct {
	ReposBase  string
	RepoPath   string
	MainBranch string
}

func FromConfig(cfg *config.Config) *Resolver {
	return &Resolver{
		ReposBase:  cfg.ReposBase,
		RepoPath:   cfg.RepoPath,
		MainBranch: cfg.MainBranch,
	}
}

func (r *Resolver) Resolve(_ context.Context, project string) (Resolved, error) {
	if r.RepoPath != "" && isGitRepo(r.RepoPath) {
		return Resolved{Path: r.RepoPath, MainBranch: r.MainBranch}, nil
	}

	if project == "" {
		return Resolved{}, &NonTransientError{Err: errors.New(
			"this ticket has no Linear project, and no REPO_PATH fallback is configured")}
	}
	return Resolved{}, &NonTransientError{Err: fmt.Errorf(
		"the Linear project %q has no `Repo:` directive, and no REPO_PATH fallback is configured",
		project)}
}

func cloneURL(ref, ownerRepo string) string {
	url := github.NormalizeRepoRef(ref)
	if !strings.Contains(url, "://") && !strings.HasPrefix(url, "git@") {
		url = "https://github.com/" + ownerRepo
	}
	return url
}

func (r *Resolver) ResolveDirect(ctx context.Context, ref, branch string) (Resolved, error) {
	ownerRepo, err := github.ExtractOwnerRepo(ref)
	if err != nil {
		return Resolved{}, &NonTransientError{Err: fmt.Errorf(
			"%q is not a valid owner/name or git URL: %w", ref, err)}
	}

	url := cloneURL(ref, ownerRepo)

	dest := filepath.Join(r.ReposBase, Slug(ownerRepo))
	if !isGitRepo(dest) {
		if err := os.MkdirAll(r.ReposBase, 0o755); err != nil {
			return Resolved{}, fmt.Errorf("mkdir %s: %w", r.ReposBase, err)
		}
		if err := checkRemoteAccess(ctx, url); err != nil {
			return Resolved{}, fmt.Errorf(
				"cannot access %q — the host running Noctra needs git auth for it "+
					"(an SSH key, or `gh auth login` for HTTPS URLs): %w", url, err)
		}
		if err := ensureCloned(ctx, url, dest); err != nil {
			return Resolved{}, fmt.Errorf("clone %s: %w", url, err)
		}
	}

	if branch == "" {
		branch = defaultBranch(ctx, dest, r.MainBranch)
	}
	return Resolved{Path: dest, MainBranch: branch}, nil
}

func defaultBranch(ctx context.Context, dir, fallback string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	out, err := cmd.Output()
	if err != nil {
		return fallback
	}
	ref := strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/")
	if ref == "" {
		return fallback
	}
	return ref
}

func checkRemoteAccess(ctx context.Context, url string) error {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", url, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func ensureCloned(ctx context.Context, url, dest string) error {
	const (
		pollInterval = 2 * time.Second
		lockTimeout  = 10 * time.Minute
	)

	if isGitRepo(dest) {
		return nil
	}

	lock := dest + ".clone-lock"
	waited := time.Duration(0)
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("acquire clone lock: %w", err)
		}
		if isGitRepo(dest) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
		waited += pollInterval
		if waited >= lockTimeout {
			return fmt.Errorf("clone lock timed out for %s", dest)
		}
	}
	defer os.Remove(lock)

	if isGitRepo(dest) {
		return nil
	}

	tmp, err := os.MkdirTemp(filepath.Dir(dest), filepath.Base(dest)+".cloning-*")
	if err != nil {
		return fmt.Errorf("create clone temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	cmd := exec.CommandContext(ctx, "git", "clone", url, tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, string(out))
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("finalize clone into %s: %w", dest, err)
	}
	return nil
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func SlugFromPath(repoPath string) string {
	return filepath.Base(repoPath)
}

func DefaultBranchOf(ctx context.Context, repoPath string) string {
	return defaultBranch(ctx, repoPath, "main")
}

func (r *Resolver) AllRepoPaths() []string {
	seen := map[string]bool{}
	var out []string

	add := func(p string) {
		if p == "" || seen[p] || !isGitRepo(p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	if entries, err := os.ReadDir(r.ReposBase); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(r.ReposBase, e.Name()))
			}
		}
	}
	add(r.RepoPath)
	return out
}

func OriginRemoteOf(ctx context.Context, repoPath string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (r *Resolver) AllRepoRemotes(ctx context.Context) []string {
	paths := r.AllRepoPaths()
	if len(paths) == 0 {
		return nil
	}

	urls := make([]string, len(paths))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			urls[idx] = OriginRemoteOf(ctx, path)
		}(i, p)
	}
	wg.Wait()

	filtered := urls[:0]
	for _, u := range urls {
		if u != "" {
			filtered = append(filtered, u)
		}
	}
	return filtered
}
