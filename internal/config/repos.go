package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// RepoEntry is one row of the repos.json registry: a Linear project name maps
// to the git URL Nightshift should clone (and optionally a per-repo main
// branch that overrides Config.MainBranch).
type RepoEntry struct {
	URL        string `json:"url"`
	MainBranch string `json:"main_branch,omitempty"`
}

// RepoRegistry is the parsed contents of repos.json.
type RepoRegistry struct {
	Repos map[string]RepoEntry `json:"repos"`
}

// LoadRepoRegistry reads and parses repos.json. A missing file is not an
// error — nil is returned so callers can decide whether the REPO_PATH
// fallback is acceptable.
func LoadRepoRegistry(path string) (*RepoRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parseRepoRegistry(data, path)
}

// ParseRepoRegistry parses an inline repos.json document (e.g. from the
// REPOS_JSON env var). Used for PaaS deploys — Fly/Render/Railway can't mount a
// file, so the registry is supplied as an environment variable instead.
func ParseRepoRegistry(data []byte) (*RepoRegistry, error) {
	return parseRepoRegistry(data, "REPOS_JSON")
}

// parseRepoRegistry unmarshals registry JSON. source is used only for error
// messages (a file path or "REPOS_JSON").
func parseRepoRegistry(data []byte, source string) (*RepoRegistry, error) {
	var r RepoRegistry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if r.Repos == nil {
		return nil, fmt.Errorf("%s has no \"repos\" object", source)
	}
	// Fail fast on a missing URL — otherwise it surfaces much later as a
	// confusing clone error when a ticket for that project comes in.
	for name, entry := range r.Repos {
		if entry.URL == "" {
			return nil, fmt.Errorf("%s: repo %q has an empty \"url\"", source, name)
		}
	}
	return &r, nil
}

// Lookup returns the entry for a project (exact match) and reports whether it
// was found. An empty project name always returns (zero, false).
func (r *RepoRegistry) Lookup(project string) (RepoEntry, bool) {
	if r == nil || project == "" {
		return RepoEntry{}, false
	}
	e, ok := r.Repos[project]
	return e, ok
}

// ProjectNames returns the registered project names in sorted order.
func (r *RepoRegistry) ProjectNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.Repos))
	for k := range r.Repos {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
