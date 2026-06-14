package pipeline

import (
	"strings"
	"testing"
)

func TestAppendCoAuthorTrailer(t *testing.T) {
	base := "feat: implement ENG-1 — title\n\nImplemented by Noctra using Claude Code\n\nLinear: https://linear.app/t/ENG-1"

	t.Run("appends trailer when non-empty", func(t *testing.T) {
		got := appendCoAuthorTrailer(base, "Claude <noreply@anthropic.com>")
		want := base + "\n\nCo-authored-by: Claude <noreply@anthropic.com>"
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("returns unchanged when empty", func(t *testing.T) {
		got := appendCoAuthorTrailer(base, "")
		if got != base {
			t.Errorf("expected message unchanged for empty coAuthor, got:\n%s", got)
		}
	})

	t.Run("trailer is last line", func(t *testing.T) {
		got := appendCoAuthorTrailer(base, "Copilot <223556219+Copilot@users.noreply.github.com>")
		lines := strings.Split(got, "\n")
		last := lines[len(lines)-1]
		if last != "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>" {
			t.Errorf("trailer should be last line, got %q", last)
		}
	})

	t.Run("trims trailing whitespace before trailer", func(t *testing.T) {
		got := appendCoAuthorTrailer(base+"\n\n", "Claude <noreply@anthropic.com>")
		want := base + "\n\nCo-authored-by: Claude <noreply@anthropic.com>"
		if got != want {
			t.Errorf("trailing whitespace not trimmed:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("iterate commit message", func(t *testing.T) {
		iterBase := "fix: address PR feedback on ENG-1\n\nFollow-up commit by Noctra (addressing review)."
		got := appendCoAuthorTrailer(iterBase, "Codex <noreply@openai.com>")
		if !strings.HasSuffix(got, "\n\nCo-authored-by: Codex <noreply@openai.com>") {
			t.Errorf("iterate commit should end with trailer, got:\n%s", got)
		}
	})
}
