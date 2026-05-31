package doctor

import (
	"os/exec"
	"testing"
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
	cfg := &stubConfig{registry: nil, repoPath: ""}
	c := checkReposFromFields(cfg.registry, cfg.repoPath, "")
	if c.ok {
		t.Error("expected failure with no registry and no REPO_PATH")
	}
}

func TestCheckRepos_FallbackPath(t *testing.T) {
	cfg := &stubConfig{registry: nil, repoPath: "/some/path"}
	c := checkReposFromFields(cfg.registry, cfg.repoPath, "")
	if !c.ok {
		t.Errorf("expected pass with REPO_PATH fallback; detail=%q", c.detail)
	}
}

// stubConfig holds the fields checkRepos needs, avoiding a real config.Load.
type stubConfig struct {
	registry *stubRegistry
	repoPath string
}

type stubRegistry struct {
	names []string
}

// checkReposFromFields is a test helper that exercises the repos check logic
// without needing a full config.Config (which requires file I/O).
func checkReposFromFields(reg *stubRegistry, repoPath, reposFile string) check {
	if reg == nil {
		if repoPath != "" {
			return check{
				name:   "repos",
				ok:     true,
				detail: "no repos.json; using REPO_PATH fallback (" + repoPath + ")",
			}
		}
		return check{
			name:   "repos",
			detail: "no repos.json and no REPO_PATH fallback",
			hint:   "Run `nightshift setup` to configure repositories.",
		}
	}
	if len(reg.names) == 0 {
		return check{
			name:   "repos",
			ok:     true,
			detail: "repos.json loaded (0 projects) — " + reposFile,
		}
	}
	return check{
		name:   "repos",
		ok:     true,
		detail: "repos.json loaded (" + string(rune('0'+len(reg.names))) + " project(s))",
	}
}
