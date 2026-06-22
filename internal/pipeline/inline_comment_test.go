package pipeline

import (
	"strings"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/review"
)

func TestInlineCommentBody(t *testing.T) {
	tests := []struct {
		name      string
		severity  string
		wantAlert string
		wantLabel string
	}{
		{"high maps to caution", "high", "[!CAUTION]", "**HIGH**"},
		{"medium maps to warning", "medium", "[!WARNING]", "**MEDIUM**"},
		{"low maps to note", "low", "[!NOTE]", "**LOW**"},
		{"unknown severity falls back to note", "blocker", "[!NOTE]", "**BLOCKER**"},
		{"empty severity falls back to review note", "", "[!NOTE]", "**REVIEW**"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := review.Finding{Severity: tt.severity, Comment: "Guard against empty input."}
			got := inlineCommentBody(f, "gemini-2.5-pro", "api")

			if !strings.HasPrefix(got, "> "+tt.wantAlert) {
				t.Errorf("body should open with %q callout, got:\n%s", tt.wantAlert, got)
			}
			if !strings.Contains(got, tt.wantLabel) {
				t.Errorf("body missing severity label %q, got:\n%s", tt.wantLabel, got)
			}
			if !strings.Contains(got, "🌙 Noctra review (Gemini `gemini-2.5-pro` via `api`)") {
				t.Errorf("body missing Noctra/Gemini attribution, got:\n%s", got)
			}
			if !strings.Contains(got, "> Guard against empty input.") {
				t.Errorf("comment should be quoted inside the callout, got:\n%s", got)
			}
		})
	}
}

func TestInlineCommentBodyQuotesMultilineComment(t *testing.T) {
	f := review.Finding{Severity: "high", Comment: "Line one.\nLine two."}
	got := inlineCommentBody(f, "gemini-2.5-pro", "api")
	if !strings.Contains(got, "> Line one.\n> Line two.") {
		t.Errorf("every comment line should be blockquoted, got:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("body should not have a trailing newline, got:\n%q", got)
	}
}
