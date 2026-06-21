package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUsesConventionalCommits(t *testing.T) {
	t.Run("config file present", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".releaserc.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !UsesConventionalCommits(dir) {
			t.Error("want true when a semantic-release config exists")
		}
	})

	t.Run("package.json reference", func(t *testing.T) {
		dir := t.TempDir()
		pkg := `{"devDependencies":{"semantic-release":"^23.0.0"}}`
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o600); err != nil {
			t.Fatal(err)
		}
		if !UsesConventionalCommits(dir) {
			t.Error("want true when package.json references semantic-release")
		}
	})

	t.Run("standard-version config file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".versionrc"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !UsesConventionalCommits(dir) {
			t.Error("want true when a .versionrc exists")
		}
	})

	t.Run("commitizen in package.json", func(t *testing.T) {
		dir := t.TempDir()
		pkg := `{"devDependencies":{"commitizen":"^4.0.0"}}`
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o600); err != nil {
			t.Fatal(err)
		}
		if !UsesConventionalCommits(dir) {
			t.Error("want true when package.json references commitizen")
		}
	})

	t.Run("goreleaser changelog grouped by conventional types", func(t *testing.T) {
		dir := t.TempDir()
		yml := "changelog:\n  groups:\n    - title: Features\n      regexp: '^feat(\\(.+\\))?!?:'\n    - title: Bug Fixes\n      regexp: '^fix(\\(.+\\))?!?:'\n"
		if err := os.WriteFile(filepath.Join(dir, ".goreleaser.yaml"), []byte(yml), 0o600); err != nil {
			t.Fatal(err)
		}
		if !UsesConventionalCommits(dir) {
			t.Error("want true when .goreleaser.yaml groups the changelog by conventional types")
		}
	})

	t.Run("goreleaser without conventional grouping", func(t *testing.T) {
		dir := t.TempDir()
		yml := "builds:\n  - main: ./cmd/app\nchangelog:\n  use: github\n"
		if err := os.WriteFile(filepath.Join(dir, ".goreleaser.yaml"), []byte(yml), 0o600); err != nil {
			t.Fatal(err)
		}
		if UsesConventionalCommits(dir) {
			t.Error("want false for a GoReleaser config without conventional changelog groups")
		}
	})

	t.Run("plain repo", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if UsesConventionalCommits(dir) {
			t.Error("want false for a repo with no CC markers")
		}
	})

	t.Run("missing repo", func(t *testing.T) {
		if UsesConventionalCommits(filepath.Join(t.TempDir(), "nope")) {
			t.Error("want false for a nonexistent path")
		}
	})
}
