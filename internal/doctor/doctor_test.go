package doctor

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/config"
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
	c := checkCLI("noctra_nonexistent_binary_xyz")
	if c.ok {
		t.Error("checkCLI should fail for a missing binary")
	}
	if c.detail != "not found in PATH" {
		t.Errorf("unexpected detail: %q", c.detail)
	}
}

func TestCheckCLI_HintsForKnownCLIs(t *testing.T) {
	for _, cli := range []string{"git", "gh", "claude", "copilot"} {
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

func TestCheckRepos_DirectiveOnly(t *testing.T) {
	// With no REPO_PATH, repos are routed entirely via Linear project `Repo:`
	// directives, so doctor must pass with an informational note.
	cfg := &config.Config{RepoPath: ""}
	c := checkRepos(cfg)
	if !c.ok {
		t.Errorf("expected pass with directive-only routing; detail=%q", c.detail)
	}
	if !strings.Contains(c.detail, "directive") && !strings.Contains(c.detail, "Repo:") {
		t.Errorf("expected detail to mention directive routing; got %q", c.detail)
	}
}

func TestCheckRepos_FallbackPath(t *testing.T) {
	cfg := &config.Config{RepoPath: "/some/path"}
	c := checkRepos(cfg)
	if !c.ok {
		t.Errorf("expected pass with REPO_PATH fallback; detail=%q", c.detail)
	}
	if !strings.Contains(c.detail, "/some/path") {
		t.Errorf("expected detail to mention the fallback path; got %q", c.detail)
	}
}

func TestCheckDashboard_Disabled(t *testing.T) {
	c := checkDashboard(&config.Config{DashboardAddr: ""})
	if !c.ok {
		t.Errorf("disabled dashboard should pass; detail=%q", c.detail)
	}
	if !strings.Contains(c.detail, "disabled") {
		t.Errorf("expected detail to say disabled; got %q", c.detail)
	}
}

func TestCheckDashboard_EnabledNoToken(t *testing.T) {
	c := checkDashboard(&config.Config{DashboardAddr: ":8080", DashboardToken: ""})
	if c.ok {
		t.Error("enabled dashboard with no token must fail (mirrors the runtime fail-fast)")
	}
	if !strings.Contains(c.detail, "DASHBOARD_TOKEN") || c.hint == "" {
		t.Errorf("expected token detail + a hint; got detail=%q hint=%q", c.detail, c.hint)
	}
}

func TestCheckDashboard_EnabledWithToken(t *testing.T) {
	c := checkDashboard(&config.Config{DashboardAddr: ":8080", DashboardToken: "secret"})
	if !c.ok {
		t.Errorf("enabled dashboard with a token should pass; detail=%q", c.detail)
	}
	if !strings.Contains(c.detail, ":8080") {
		t.Errorf("expected detail to mention the address; got %q", c.detail)
	}
}

func TestCheckDashboard_ExposedBindWarns(t *testing.T) {
	c := checkDashboard(&config.Config{DashboardAddr: "0.0.0.0:8080", DashboardToken: "secret"})
	if !c.ok {
		t.Errorf("a 0.0.0.0 bind with a token still passes; detail=%q", c.detail)
	}
	if !strings.Contains(c.detail, "all interfaces") {
		t.Errorf("expected an exposure warning in detail; got %q", c.detail)
	}
}

func TestRunJSON_Shape(t *testing.T) {
	// Use a temp dir with no config: gather() still emits checks (CLIs, gh
	// auth, config error, config dir), so we get a non-empty, well-formed array.
	var buf bytes.Buffer
	_ = RunJSON(t.TempDir(), &buf)

	var got []jsonCheck
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(got) == 0 {
		t.Fatal("expected at least one check in JSON output")
	}
	for i, c := range got {
		if c.Name == "" {
			t.Errorf("check[%d] has empty name", i)
		}
	}

	// The "config dir" check must always be present and report the dir.
	found := false
	for _, c := range got {
		if c.Name == "config dir" {
			found = true
			if !c.OK {
				t.Error("config dir check should be ok")
			}
		}
	}
	if !found {
		t.Error("expected a 'config dir' check in JSON output")
	}

	// Verify the JSON keys match the documented {name, ok, detail, hint} shape.
	var raw []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("re-unmarshal failed: %v", err)
	}
	for _, m := range raw {
		if _, ok := m["name"]; !ok {
			t.Errorf("object missing 'name' key: %v", m)
		}
		if _, ok := m["ok"]; !ok {
			t.Errorf("object missing 'ok' key: %v", m)
		}
	}
}

func TestRunJSON_ReturnsErrorOnFailure(t *testing.T) {
	// Force a deterministic failure: clear LINEAR_API_KEY so an ambient key in
	// the environment can't satisfy the Linear check, and point at an empty temp
	// config dir. The Linear-key (or config-load) check then fails → non-nil
	// error, regardless of which CLIs happen to be on PATH.
	t.Setenv("LINEAR_API_KEY", "")
	var buf bytes.Buffer
	err := RunJSON(t.TempDir(), &buf)
	if err == nil {
		t.Error("expected an error when a check fails")
	}
	// JSON must still have been written even on failure.
	if buf.Len() == 0 {
		t.Error("expected JSON output even when checks fail")
	}
}

func TestGather_AgentBackend(t *testing.T) {
	tests := []struct {
		backend string
		cli     string
	}{
		{"antigravity", "agy"},
		{"claude", "claude"},
		{"copilot", "copilot"},
		{"codex", "codex"},
	}

	for _, tc := range tests {
		t.Run(tc.backend, func(t *testing.T) {
			t.Setenv("AGENT_BACKEND", tc.backend)

			checks := gather(t.TempDir())

			backendIndex := -1
			cliIndex := -1

			for i, c := range checks {
				if c.name == "agent backend" {
					backendIndex = i
					if !c.ok {
						t.Error("expected agent backend check to be ok=true")
					}
					expectedDetail := tc.backend + " (" + tc.cli + ")"
					if c.detail != expectedDetail {
						t.Errorf("expected detail %q, got %q", expectedDetail, c.detail)
					}
				}
				if c.name == tc.cli {
					cliIndex = i
				}
			}

			if backendIndex == -1 {
				t.Error("expected an 'agent backend' check in gather() results")
			}
			if cliIndex == -1 {
				t.Error("expected CLI check in gather() results")
			}

			if backendIndex != -1 && cliIndex != -1 {
				if backendIndex != cliIndex-1 {
					t.Errorf("expected 'agent backend' check to be directly before '%s' check, but backend index is %d and CLI index is %d", tc.cli, backendIndex, cliIndex)
				}
			}
		})
	}
}

func TestGather_AgentBackend_Invalid(t *testing.T) {
	t.Setenv("AGENT_BACKEND", "invalidbackend")

	checks := gather(t.TempDir())

	found := false
	for _, c := range checks {
		if c.name == "agent backend" {
			found = true
			if c.ok {
				t.Error("expected agent backend check to fail for invalid backend")
			}
			if !strings.Contains(c.detail, "unsupported") {
				t.Errorf("expected detail to mention unsupported, got %q", c.detail)
			}
			if !strings.Contains(c.hint, "must be") {
				t.Errorf("expected hint to instruct valid choices, got %q", c.hint)
			}
		}
	}

	if !found {
		t.Error("expected an 'agent backend' check in gather() results")
	}
}
