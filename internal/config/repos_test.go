package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRepoRegistry(t *testing.T) {
	r, err := ParseRepoRegistry([]byte(`{"repos":{"Web App":{"url":"https://github.com/me/web.git"}}}`))
	if err != nil {
		t.Fatalf("ParseRepoRegistry: %v", err)
	}
	e, ok := r.Lookup("Web App")
	if !ok || e.URL != "https://github.com/me/web.git" {
		t.Errorf("Lookup: got %+v ok=%v", e, ok)
	}
	if _, err := ParseRepoRegistry([]byte(`not json`)); err == nil {
		t.Error("expected error on invalid JSON")
	}
	if _, err := ParseRepoRegistry([]byte(`{}`)); err == nil {
		t.Error("expected error when \"repos\" object is missing")
	}
}

func TestLoad_ReposJSONEnvTakesPrecedence(t *testing.T) {
	isolateEnv(t)
	t.Setenv("REPOS_JSON", `{"repos":{"Inline Project":{"url":"https://github.com/me/inline.git"}}}`)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"`)
	// A repos.json file exists but REPOS_JSON should win.
	writeFile(t, filepath.Join(dir, "repos.json"), `{"repos":{"File Project":{"url":"https://github.com/me/file.git"}}}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Registry.Lookup("Inline Project"); !ok {
		t.Error("REPOS_JSON registry should be loaded")
	}
	if _, ok := cfg.Registry.Lookup("File Project"); ok {
		t.Error("repos.json file should be ignored when REPOS_JSON is set")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestLoadRepoRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.json")

	body := `{
	  "_comment": "ignored top-level field",
	  "repos": {
	    "Auth Service": { "url": "git@github.com:me/auth.git", "main_branch": "develop" },
	    "Web App": { "url": "https://github.com/me/web.git" }
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := LoadRepoRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r == nil {
		t.Fatal("registry is nil")
	}

	entry, ok := r.Lookup("Auth Service")
	if !ok {
		t.Fatal("expected Auth Service to be found")
	}
	if entry.URL != "git@github.com:me/auth.git" {
		t.Errorf("url: got %q", entry.URL)
	}
	if entry.MainBranch != "develop" {
		t.Errorf("main_branch: got %q", entry.MainBranch)
	}

	if _, ok := r.Lookup("Web App"); !ok {
		t.Errorf("expected Web App to be found")
	}
	if _, ok := r.Lookup("Nonexistent"); ok {
		t.Errorf("Nonexistent should not be found")
	}
	if _, ok := r.Lookup(""); ok {
		t.Errorf("empty project name should not match")
	}

	names := r.ProjectNames()
	if len(names) != 2 || names[0] != "Auth Service" || names[1] != "Web App" {
		t.Errorf("project names: got %v", names)
	}
}

func TestLoadRepoRegistry_Missing(t *testing.T) {
	r, err := LoadRepoRegistry(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if r != nil {
		t.Fatalf("expected nil registry, got %+v", r)
	}
}

func TestLoadRepoRegistry_NilRegistryLookup(t *testing.T) {
	var r *RepoRegistry
	if _, ok := r.Lookup("anything"); ok {
		t.Fatal("nil registry should not produce hits")
	}
	if names := r.ProjectNames(); names != nil {
		t.Fatalf("nil registry project names: got %v", names)
	}
}
