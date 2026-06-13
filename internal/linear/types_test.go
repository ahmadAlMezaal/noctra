package linear

import (
	"reflect"
	"testing"
)

func TestClarificationComments_FiltersSystemComments(t *testing.T) {
	issue := Issue{
		Comments: CommentConnection{Nodes: []Comment{
			// Linear↔GitHub sync notice — filtered.
			{Body: "This comment thread is synced to a corresponding GitHub issue."},
			// A genuine human clarification — kept.
			{Body: "Use https://getnightshift.dev as the canonical URL.", User: &User{Name: "Ahmad"}},
			// Nightshift's own BLOCKED notification — filtered (would otherwise be
			// echoed back at the agent).
			{Body: "🚧 **Nightshift needs your input** (attempt 1/3)\n\nThe agent got blocked..."},
			// Whitespace-only — skipped.
			{Body: "   "},
			// Author missing — kept, attributed to "Someone".
			{Body: "Also add a badge near the title."},
		}},
	}

	got := issue.ClarificationComments()
	want := []string{
		"Ahmad: Use https://getnightshift.dev as the canonical URL.",
		"Someone: Also add a badge near the title.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ClarificationComments:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestClarificationComments_EmptyWhenNoComments(t *testing.T) {
	if got := (Issue{}).ClarificationComments(); len(got) != 0 {
		t.Errorf("expected no comments, got %#v", got)
	}
}
