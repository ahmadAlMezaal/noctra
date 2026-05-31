package agent

import (
	"fmt"
	"strings"
)

// FeedbackItem is one piece of feedback rendered into a fix prompt — a
// conversation comment or a review body. Kept stringly-typed so the agent
// package doesn't have to depend on internal/github or internal/watch.
type FeedbackItem struct {
	// Kind is "comment" or "review" — controls the section heading.
	Kind string
	// Author is the GitHub login that posted it.
	Author string
	// Body is the markdown body.
	Body string
	// State is "" for comments; for reviews it's APPROVED /
	// CHANGES_REQUESTED / COMMENTED — rendered alongside the author for
	// extra context.
	State string
	// URL points back to the comment on GitHub (optional, comments only).
	URL string
	// Path / Line locate an inline review comment in the diff. Empty for
	// conversation comments and reviews.
	Path string
	Line int
}

// CIItem is one failing CI check rendered into a fix prompt.
type CIItem struct {
	// Name is the check / workflow name (e.g. "build", "lint").
	Name string
	// URL links to the check run (optional).
	URL string
	// Logs is the truncated failed-step log tail (may be empty if the logs
	// couldn't be fetched — Claude can still reproduce locally).
	Logs string
}

// FixPromptInput is everything BuildFixPrompt needs to assemble a prompt the
// implementing agent can act on without re-reading the PR.
type FixPromptInput struct {
	Identifier  string
	Title       string
	Description string
	PRNumber    int
	PRURL       string
	Feedback    []FeedbackItem
	CI          []CIItem
}

// BuildFixPrompt renders a fix prompt for Claude when reviewers (or bots in
// the trust list) have left actionable feedback on the PR for this ticket.
// The prompt explicitly instructs Claude to address ONLY the listed feedback
// and not to do unrelated work — the goal is a tight follow-up commit, not
// a do-over.
func BuildFixPrompt(in FixPromptInput) string {
	desc := in.Description
	if desc == "" {
		desc = "No description provided."
	}

	var sections strings.Builder
	if len(in.Feedback) > 0 {
		sections.WriteString("## Review feedback to address\n\n")
		for i, f := range in.Feedback {
			fmt.Fprintf(&sections, "### %d) %s by @%s\n", i+1, sectionLabel(f.Kind, f.State), f.Author)
			if f.Path != "" {
				if f.Line > 0 {
					fmt.Fprintf(&sections, "on `%s:%d`\n", f.Path, f.Line)
				} else {
					fmt.Fprintf(&sections, "on `%s`\n", f.Path)
				}
			}
			if f.URL != "" {
				fmt.Fprintf(&sections, "(%s)\n", f.URL)
			}
			sections.WriteByte('\n')
			sections.WriteString(strings.TrimSpace(f.Body))
			sections.WriteString("\n\n")
		}
	}
	if len(in.CI) > 0 {
		sections.WriteString("## Failing CI checks to fix\n\n")
		for i, c := range in.CI {
			fmt.Fprintf(&sections, "### %d) %s\n", i+1, c.Name)
			if c.URL != "" {
				fmt.Fprintf(&sections, "(%s)\n", c.URL)
			}
			if logs := strings.TrimSpace(c.Logs); logs != "" {
				fmt.Fprintf(&sections, "\n```\n%s\n```\n", logs)
			}
			sections.WriteByte('\n')
		}
	}

	return fmt.Sprintf(`There is new activity on the PR you opened for this Linear ticket — review feedback and/or failing CI. Address it on the same branch; your changes will be pushed as a follow-up commit.

## Ticket: %s — %s
%s

## PR
#%d — %s

%s## Rules

- Address ONLY the feedback and CI failures listed above. Do not refactor unrelated code or pick up new work.
- If CI is failing, reproduce it locally (run the relevant tests / linter), fix the root cause, and re-run to confirm it passes.
- If a piece of feedback is wrong or inapplicable, briefly say so and skip it — do not silently ignore it.
- Run the test suite and the linter (`+"`golangci-lint run`"+`, if configured) after your changes; fix anything you broke.
- If you cannot proceed because more context is needed, say BLOCKED: <reason> and stop.
- Do NOT create a new PR, push a new branch, or close the existing PR — Nightshift handles that.

## When done

Summarise what you addressed and how, and call out anything you deliberately skipped (with the reason).
`, in.Identifier, in.Title, desc, in.PRNumber, in.PRURL, sections.String())
}

func sectionLabel(kind, state string) string {
	switch kind {
	case "review":
		if state != "" {
			return "Review (" + state + ")"
		}
		return "Review"
	default:
		return "Comment"
	}
}
