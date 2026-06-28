package agent

import (
	"fmt"
	"strings"
)

// FeedbackItem is one piece of feedback rendered into a fix prompt (a comment or review body). Stringly-typed so agent needn't depend on internal/github or internal/watch.
type FeedbackItem struct {
	// Kind is "comment" or "review" — controls the section heading.
	Kind string
	// Author is the GitHub login that posted it.
	Author string
	// Body is the markdown body.
	Body string
	// State is "" for comments; APPROVED/CHANGES_REQUESTED/COMMENTED for reviews.
	State string
	// URL points back to the comment on GitHub (optional, comments only).
	URL string
	// Path / Line locate an inline review comment in the diff (empty otherwise).
	Path string
	Line int
}

// CIItem is one failing CI check rendered into a fix prompt.
type CIItem struct {
	// Name is the check / workflow name (e.g. "build", "lint").
	Name string
	// URL links to the check run (optional).
	URL string
	// Logs is the truncated failed-step log tail (may be empty; the agent can reproduce locally).
	Logs string
}

// FixPromptInput is everything BuildFixPrompt needs to assemble a prompt the agent can act on without re-reading the PR.
type FixPromptInput struct {
	Identifier  string
	Title       string
	Description string
	PRNumber    int
	PRURL       string
	Feedback    []FeedbackItem
	CI          []CIItem
	RepoLessons string // per-repo lessons
	// PriorReasoning is the agent's summary from the previous re-engagement, so it doesn't re-litigate settled feedback.
	PriorReasoning string
}

// BuildFixPrompt renders a fix prompt for actionable PR feedback/CI, instructing the agent to address ONLY the listed items — a tight follow-up commit, not a do-over.
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

	lessonsSection := ""
	if in.RepoLessons != "" {
		lessonsSection = "\n\n## Repository Lessons & Conventions (from post-merge human edits to previous PRs):\n" +
			in.RepoLessons + "\n"
	}

	priorSection := ""
	if r := strings.TrimSpace(in.PriorReasoning); r != "" {
		priorSection = "\n\n## Your notes from the previous iteration on this PR\n" + r +
			"\nDon't redo or re-argue anything already settled here unless the new feedback contradicts it.\n"
	}

	findingsSection := ""
	if len(in.Feedback) > 0 {
		findingsSection = fmt.Sprintf(`

Then, after the summary, report on each numbered review finding above so Noctra can reply to that exact review thread. Wrap a JSON array between %s and %s:

%s
[
  {"finding": 1, "addressed": true, "reply": "Narrowed the regex to 40+ hex chars so legitimate IDs aren't redacted."},
  {"finding": 2, "addressed": false, "reply": "Kept the dual-token check by design — the read/admin split still holds; explained why."}
]
%s

- `+"`finding`"+` is the number from the "Review feedback to address" list.
- `+"`addressed`"+` is true if you changed code for it, false if you pushed back or judged it inapplicable.
- `+"`reply`"+` is one plain sentence posted on that finding's thread — no markdown headings. Include an entry for every numbered finding.
`, FindingsStartMarker, FindingsEndMarker, FindingsStartMarker, FindingsEndMarker)
	}

	return fmt.Sprintf(`There is new activity on the PR you opened for this Linear ticket — review feedback and/or failing CI. Address it on the same branch; your changes will be pushed as a follow-up commit.

## Ticket: %s — %s
%s

## PR
#%d — %s

%s%s%s## Rules

- Address ONLY the feedback and CI failures listed above. Do not refactor unrelated code or pick up new work.
- If CI is failing, reproduce it locally (run the relevant tests / linter), fix the root cause, and re-run to confirm it passes.
- If a piece of feedback is wrong or inapplicable, briefly say so and skip it — do not silently ignore it.
- Run the test suite and the linter (`+"`golangci-lint run`"+`, if configured) after your changes; fix anything you broke.
- If you cannot proceed because more context is needed, say BLOCKED: <reason> and stop.
- Do NOT create a new PR, push a new branch, or close the existing PR — Noctra handles that.

## When done

Wrap a short summary between %s and %s. Say what you addressed and how, and call out anything you deliberately skipped or pushed back on (with the reason) — this is posted back on the PR for the reviewer.
%s`, in.Identifier, in.Title, desc, in.PRNumber, in.PRURL, lessonsSection, priorSection, sections.String(), SummaryStartMarker, SummaryEndMarker, findingsSection)
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
