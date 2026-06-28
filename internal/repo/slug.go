// Package repo resolves which git repository a Linear ticket targets and manages the per-ticket worktree lifecycle.
package repo

import "strings"

// Slug turns a project name into a filesystem-safe clone directory name: non-alphanumerics collapse to single dashes, edges trimmed (e.g. "My API  v2" → "my-api-v2").
func Slug(name string) string {
	var b strings.Builder
	b.Grow(len(name))

	prevDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
