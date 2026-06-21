package repo

import (
	"os"
	"path/filepath"
	"strings"
)

// ccConfigFiles signal a repo uses a Conventional Commits / release tool
// (commitlint, semantic-release, commitizen, standard-version).
var ccConfigFiles = []string{
	".releaserc", ".releaserc.json", ".releaserc.yaml", ".releaserc.yml",
	".releaserc.js", ".releaserc.cjs", "release.config.js", "release.config.cjs",
	"commitlint.config.js", "commitlint.config.cjs", "commitlint.config.ts",
	".commitlintrc", ".commitlintrc.json", ".commitlintrc.yaml",
	".commitlintrc.yml", ".commitlintrc.js", ".commitlintrc.cjs",
	".czrc", ".cz.json", ".versionrc", ".versionrc.json", ".versionrc.js",
}

// ccPackageRefs are package.json substrings indicating a CC/release tool.
var ccPackageRefs = []string{"semantic-release", "commitlint", "commitizen", "standard-version"}

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
		for _, ref := range ccPackageRefs {
			if strings.Contains(s, ref) {
				return true
			}
		}
	}
	return false
}
