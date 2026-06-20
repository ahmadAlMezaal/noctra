package repo

import (
	"os"
	"path/filepath"
	"strings"
)

// ccConfigFiles signal a repo uses commitlint or semantic-release.
var ccConfigFiles = []string{
	".releaserc", ".releaserc.json", ".releaserc.yaml", ".releaserc.yml",
	".releaserc.js", ".releaserc.cjs", "release.config.js", "release.config.cjs",
	"commitlint.config.js", "commitlint.config.cjs", "commitlint.config.ts",
	".commitlintrc", ".commitlintrc.json", ".commitlintrc.yaml",
	".commitlintrc.yml", ".commitlintrc.js", ".commitlintrc.cjs",
}

// UsesConventionalCommits reports whether the repo at repoPath uses
// Conventional Commits, detected via config file or package.json reference.
func UsesConventionalCommits(repoPath string) bool {
	for _, name := range ccConfigFiles {
		if _, err := os.Stat(filepath.Join(repoPath, name)); err == nil {
			return true
		}
	}
	if data, err := os.ReadFile(filepath.Join(repoPath, "package.json")); err == nil {
		s := string(data)
		if strings.Contains(s, "semantic-release") || strings.Contains(s, "commitlint") {
			return true
		}
	}
	return false
}
