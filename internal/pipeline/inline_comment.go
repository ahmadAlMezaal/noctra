package pipeline

import (
	"fmt"
	"strings"

	"github.com/ahmadAlMezaal/noctra/internal/review"
)

// inlineCommentBody renders a review finding as a GitHub-flavoured inline
// comment: a severity-coloured alert callout (CAUTION/WARNING/NOTE) carrying a
// Noctra/Gemini attribution header, with the model's comment quoted inside it.
// The attribution makes clear the comment is automated review output rather
// than the operator's own (the Pi posts under a personal GitHub account until a
// dedicated bot identity exists). PostInlineComments appends the hidden reply
// marker, so this body must not.
func inlineCommentBody(f review.Finding, model, mode string) string {
	alert, label := "NOTE", "REVIEW"
	switch strings.ToLower(f.Severity) {
	case "high":
		alert, label = "CAUTION", "HIGH"
	case "medium":
		alert, label = "WARNING", "MEDIUM"
	case "low":
		alert, label = "NOTE", "LOW"
	default:
		if f.Severity != "" {
			label = strings.ToUpper(f.Severity)
		}
	}

	header := fmt.Sprintf("**%s** · 🌙 Noctra review (Gemini `%s` via `%s`)", label, model, mode)

	var b strings.Builder
	fmt.Fprintf(&b, "> [!%s]\n> %s\n>\n", alert, header)
	for _, line := range strings.Split(strings.TrimSpace(f.Comment), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
