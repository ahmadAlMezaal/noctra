package doctor

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
)

func TestCheckCLI_Found(t *testing.T) {
	// "go" is guaranteed to be on PATH in a Go test.
	c := checkCLI("go")
	if !c.ok {
		t.Errorf("checkCLI(go) should pass; got detail=%q", c.detail)
	}
	if c.detail == "" {
		t.Error("expected non-empty detail (path)")
	}
}

func TestCheckCLI_NotFound(t *testing.T) {
	c := checkCLI("nightshift_nonexistent_binary_xyz")
	if c.ok {
		t.Error("checkCLI should fail for a missing binary")
	}
	if c.detail != "not found in PATH" {
		t.Errorf("unexpected detail: %q", c.detail)
	}
}

func TestCheckCLI_HintsForKnownCLIs(t *testing.T) {
	for _, cli := range []string{"git", "gh", "claude"} {
		c := checkCLI(cli)
		// We can't control whether these are installed, but we can verify
		// the hint is populated when missing.
		if !c.ok && c.hint == "" {
			t.Errorf("checkCLI(%s) failed but has no hint", cli)
		}
	}
}

func TestCheckGHAuth_Runs(t *testing.T) {
	// This test just verifies checkGHAuth doesn't panic.
	// The actual result depends on whether gh is installed + authenticated.
	c := checkGHAuth()
	if _, err := exec.LookPath("gh"); err != nil {
		// gh not installed — should be skipped
		if c.ok {
			t.Error("checkGHAuth should not pass when gh is not installed")
		}
		if c.detail != "skipped (gh not installed)" {
			t.Errorf("unexpected detail when gh missing: %q", c.detail)
		}
	}
	// If gh is installed, we can't predict auth state — just ensure no crash.
}

func TestCheckRepos_NilRegistry(t *testing.T) {
	// Missing repos.json is non-fatal: repos are routed via Linear project
	// `Repo:` directives, so doctor must pass (with an informational note).
	cfg := &config.Config{Registry: nil, RepoPath: ""}
	c := checkRepos(cfg)
	if !c.ok {
		t.Errorf("expected pass with no registry (directive-first routing); detail=%q", c.detail)
	}
	if !strings.Contains(c.detail, "directive") && !strings.Contains(c.detail, "Repo:") {
		t.Errorf("expected detail to mention directive routing; got %q", c.detail)
	}
}

func TestCheckRepos_FallbackPath(t *testing.T) {
	cfg := &config.Config{Registry: nil, RepoPath: "/some/path"}
	c := checkRepos(cfg)
	if !c.ok {
		t.Errorf("expected pass with REPO_PATH fallback; detail=%q", c.detail)
	}
}

func TestCheckRepos_WithProjects(t *testing.T) {
	repos := make(map[string]config.RepoEntry)
	for i := 0; i < 12; i++ {
		repos["project-"+strings.Repeat("x", i+1)] = config.RepoEntry{URL: "https://example.com"}
	}
	cfg := &config.Config{
		Registry:  &config.RepoRegistry{Repos: repos},
		ReposFile: "/path/to/repos.json",
	}
	c := checkRepos(cfg)
	if !c.ok {
		t.Errorf("expected pass with 12 projects; detail=%q", c.detail)
	}
	if !strings.Contains(c.detail, "12 project(s)") {
		t.Errorf("expected detail to contain '12 project(s)'; got %q", c.detail)
	}
}
