package pipeline

import (
	"fmt"
	"strings"

	"github.com/ahmadAlMezaal/noctra/internal/review"
)

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
