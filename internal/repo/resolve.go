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

	"github.com/ahmadAlMezaal/nightshift/internal/config"
	"github.com/ahmadAlMezaal/nightshift/internal/github"
)

// NonTransientError signals a failure that is deterministic and cannot
// resolve without human intervention — e.g. a ticket whose Linear project has
// no "Repo:" directive and no REPO_PATH fallback, or an unparseable directive.
// The pipeline uses this to skip the ticket on future polls instead of
// retrying every cycle and burning the dispatch budget.
type NonTransientError struct {
	Err error
}

func (e *NonTransientError) Error() string { return e.Err.Error() }
func (e *NonTransientError) Unwrap() error { return e.Err }

// Resolved describes the local repository and base branch a ticket should
// be implemented against.
type Resolved struct {
	Path       string
	MainBranch string
}

// Resolver clones a ticket's repo on demand into ReposBase. Repos are routed
// per-ticket from each Linear project's "Repo:" directive (ResolveDirect);
// RepoPath is an optional single-repo fallback (Resolve) for tickets whose
// project declares no directive.
type Resolver struct {
	ReposBase  string
	RepoPath   string // optional single-repo fallback
	MainBranch string // default main branch
}

// FromConfig builds a Resolver from a config.Config.
func FromConfig(cfg *config.Config) *Resolver {
	return &Resolver{
		ReposBase:  cfg.ReposBase,
		RepoPath:   cfg.RepoPath,
		MainBranch: cfg.MainBranch,
	}
}

// Resolve is the fallback path for tickets whose Linear project has no "Repo:"
// directive: it returns the REPO_PATH single-repo fallback when configured, and
// otherwise a NonTransientError so the pipeline skips the ticket. The directive
// route is handled by ResolveDirect.
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

// ResolveDirect locates (cloning on demand) a repo named explicitly — by a
// Linear project's "Repo:" directive or, in the auto-iterate path, by a PR's own
// repository. ref may be an "owner/name" shorthand (assumed GitHub) or a full
// https/ssh git URL. branch overrides the base branch; when empty, the repo's
// actual default branch (read after clone) is used, falling back to MainBranch.
func (r *Resolver) ResolveDirect(ctx context.Context, ref, branch string) (Resolved, error) {
	ownerRepo, err := github.ExtractOwnerRepo(ref)
	if err != nil {
		return Resolved{}, &NonTransientError{Err: fmt.Errorf(
			"%q is not a valid owner/name or git URL: %w", ref, err)}
	}

	url := strings.TrimSpace(ref)
	if !strings.Contains(url, "://") && !strings.HasPrefix(url, "git@") {
		url = "https://github.com/" + ownerRepo
	}

	dest := filepath.Join(r.ReposBase, Slug(ownerRepo))
	if !isGitRepo(dest) {
		if err := os.MkdirAll(r.ReposBase, 0o755); err != nil {
			return Resolved{}, fmt.Errorf("mkdir %s: %w", r.ReposBase, err)
		}
		if err := checkRemoteAccess(ctx, url); err != nil {
			return Resolved{}, fmt.Errorf(
				"cannot access %q — the host running Nightshift needs git auth for it "+
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

// defaultBranch reads the repo's default branch from origin/HEAD (set by clone),
// falling back to fallback when it can't be determined.
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

// checkRemoteAccess runs `git ls-remote` to verify the host can reach the
// remote. Fast (no clone) and gives a clear failure before we commit to a
// long clone.
func checkRemoteAccess(ctx context.Context, url string) error {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", url, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

// ensureCloned clones url into dest if it isn't already a git repo. An mkdir
// lock at <dest>.clone-lock serializes concurrent attempts (mkdir is atomic
// on POSIX, so it's a portable lock primitive).
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
		// Lock is held — either we'll inherit the result or time out.
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

	// Re-check after acquiring — another caller may have finished the clone
	// just before we got the lock.
	if isGitRepo(dest) {
		return nil
	}

	// Clone into a temp dir, then atomically rename into place. `git clone`
	// creates dest/.git early (before objects/refs are fetched), so cloning
	// straight into dest would make isGitRepo(dest) true mid-clone — and a
	// concurrent Resolve (or the lock-loser's poll) would then use a partial
	// repo whose origin/<main> doesn't exist yet. The rename makes dest appear
	// only once the clone is complete. (Same temp-then-rename trick as
	// state.writeAtomic; the temp lives beside dest so the rename is atomic.)
	tmp, err := os.MkdirTemp(filepath.Dir(dest), filepath.Base(dest)+".cloning-*")
	if err != nil {
		return fmt.Errorf("create clone temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() // no-op once renamed away; cleans up on any failure

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

// AllRepoPaths returns every local repo Nightshift knows about: every on-demand
// clone found under ReposBase, plus the REPO_PATH fallback (if set and valid).
// Used by the cleanup subcommand and startup cleanup. Directive-only routing
// means there's no static registry, so the cloned repos are discovered by
// scanning ReposBase rather than enumerating configured project names.
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

// AllRepoRemotes returns the `origin` remote URL of every local repo from
// AllRepoPaths. The PR watcher uses this to discover which repos to scan for
// Nightshift-authored PRs — with directive-only routing the set of repos is
// whatever has been cloned so far, so it's derived from the clones on disk.
//
// The git reads run concurrently and honour ctx: this is called on the PR-poll
// path (under that poll's timeout), so a single hung `git` must neither block
// the loop nor outlive the scan. Results are written to fixed indices to keep
// the order stable without a lock.
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
			out, err := exec.CommandContext(ctx, "git", "-C", path, "remote", "get-url", "origin").Output()
			if err == nil {
				urls[idx] = strings.TrimSpace(string(out))
			}
		}(i, p)
	}
	wg.Wait()

	filtered := urls[:0] // reuse backing array — order preserved, empties dropped
	for _, u := range urls {
		if u != "" {
			filtered = append(filtered, u)
		}
	}
	return filtered
}
