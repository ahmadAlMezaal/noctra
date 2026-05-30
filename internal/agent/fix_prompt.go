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

	var feedback strings.Builder
	for i, f := range in.Feedback {
		header := fmt.Sprintf("### %d) %s by @%s", i+1, sectionLabel(f.Kind, f.State), f.Author)
		feedback.WriteString(header)
		feedback.WriteByte('\n')
		if f.URL != "" {
			fmt.Fprintf(&feedback, "(%s)\n", f.URL)
		}
		feedback.WriteByte('\n')
		feedback.WriteString(strings.TrimSpace(f.Body))
		feedback.WriteString("\n\n")
	}

	return fmt.Sprintf(`A reviewer has left feedback on the PR you opened for this Linear ticket. Address it on the same branch — your changes will be pushed as a follow-up commit.

## Ticket: %s — %s
%s

## PR
#%d — %s

## Feedback to address

%s
## Rules

- Address ONLY the feedback listed above. Do not refactor unrelated code or pick up new work.
- If a piece of feedback is wrong or inapplicable, briefly say so and skip it — do not silently ignore it.
- Run the test suite and the linter (`+"`golangci-lint run`"+`, if configured) after your changes; fix anything you broke.
- If you cannot address a piece of feedback because more context is needed, say BLOCKED: <reason> and stop.
- Do NOT create a new PR, push a new branch, or close the existing PR — Nightshift handles that.

## When done

Summarise which pieces of feedback you addressed and how, and call out any you deliberately skipped (with the reason).
`, in.Identifier, in.Title, desc, in.PRNumber, in.PRURL, feedback.String())
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
