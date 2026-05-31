package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
)

// Resolved describes the local repository and base branch a ticket should
// be implemented against.
type Resolved struct {
	Path       string
	MainBranch string
}

// Resolver looks up a Linear project's repo and (if needed) clones it on
// demand into ReposBase. If the project isn't in the registry, RepoPath is
// used as a fallback.
type Resolver struct {
	Registry   *config.RepoRegistry
	ReposBase  string
	RepoPath   string // optional single-repo fallback
	MainBranch string // default main branch
}

// FromConfig builds a Resolver from a config.Config.
func FromConfig(cfg *config.Config) *Resolver {
	return &Resolver{
		Registry:   cfg.Registry,
		ReposBase:  cfg.ReposBase,
		RepoPath:   cfg.RepoPath,
		MainBranch: cfg.MainBranch,
	}
}

// Resolve maps a Linear project name onto a local git repo. When the project
// has a registry entry but its repo isn't cloned yet, Resolve verifies remote
// access and clones it into ReposBase/<slug>.
//
// The mkdir-based lock at <dest>.clone-lock keeps two concurrent tickets from
// racing the same initial clone.
func (r *Resolver) Resolve(ctx context.Context, project string) (Resolved, error) {
	if entry, ok := r.Registry.Lookup(project); ok {
		branch := entry.MainBranch
		if branch == "" {
			branch = r.MainBranch
		}

		dest := filepath.Join(r.ReposBase, Slug(project))
		if !isGitRepo(dest) {
			if err := os.MkdirAll(r.ReposBase, 0o755); err != nil {
				return Resolved{}, fmt.Errorf("mkdir %s: %w", r.ReposBase, err)
			}
			if err := checkRemoteAccess(ctx, entry.URL); err != nil {
				return Resolved{}, fmt.Errorf(
					"cannot access %q — the host running Nightshift needs git auth for it "+
						"(an SSH key, or `gh auth login` for HTTPS URLs): %w", entry.URL, err)
			}
			if err := ensureCloned(ctx, entry.URL, dest); err != nil {
				return Resolved{}, fmt.Errorf("clone %s: %w", entry.URL, err)
			}
		}
		return Resolved{Path: dest, MainBranch: branch}, nil
	}

	if r.RepoPath != "" && isGitRepo(r.RepoPath) {
		return Resolved{Path: r.RepoPath, MainBranch: r.MainBranch}, nil
	}

	if project == "" {
		return Resolved{}, errors.New(
			"this ticket has no Linear project, and no REPO_PATH fallback is configured")
	}
	return Resolved{}, fmt.Errorf(
		"no repo is mapped for the project %q in repos.json, and no REPO_PATH fallback is configured",
		project)
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
	defer os.RemoveAll(tmp) // no-op once renamed away; cleans up on any failure

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

// AllRepoPaths returns every local repo Nightshift knows about: each registry
// entry whose clone exists, plus the REPO_PATH fallback (if set and valid).
// Used by the cleanup subcommand.
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

	if r.Registry != nil {
		for _, name := range r.Registry.ProjectNames() {
			add(filepath.Join(r.ReposBase, Slug(name)))
		}
	}
	add(r.RepoPath)
	return out
}
