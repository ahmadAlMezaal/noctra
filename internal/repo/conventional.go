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

// UsesConventionalCommits reports whether the repo uses Conventional Commits,
// detected via a CC/release config file, package.json, or GoReleaser changelog.
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
	return goreleaserUsesConventional(repoPath)
}

// goreleaserUsesConventional reports whether a GoReleaser changelog is grouped
// by conventional types. Both ^feat and ^fix are required so plain configs don't match.
func goreleaserUsesConventional(repoPath string) bool {
	for _, name := range []string{".goreleaser.yaml", ".goreleaser.yml"} {
		data, err := os.ReadFile(filepath.Join(repoPath, name))
		if err != nil {
			continue
		}
		s := string(data)
		if strings.Contains(s, "changelog:") &&
			strings.Contains(s, "^feat") && strings.Contains(s, "^fix") {
			return true
		}
	}
	return false
}
