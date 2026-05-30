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

	var r RepoRegistry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if r.Repos == nil {
		return nil, fmt.Errorf("%s has no \"repos\" object", path)
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
