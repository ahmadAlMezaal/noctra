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
			// Human reply that QUOTES the bot notification, then adds their own
			// clarification — must be kept, not mistaken for a system comment.
			{Body: "> 🚧 **Nightshift needs your input**\n\nActually, use the other URL.", User: &User{Name: "Ahmad"}},
		}},
	}

	got := issue.ClarificationComments()
	want := []string{
		"Ahmad: Use https://getnightshift.dev as the canonical URL.",
		"Someone: Also add a badge near the title.",
		"Ahmad: > 🚧 **Nightshift needs your input**\n\nActually, use the other URL.",
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

func TestProjectRepoDirective(t *testing.T) {
	cases := []struct {
		name             string
		desc             string
		wantRepo, wantBr string
	}{
		{"none", "Just a normal project summary.", "", ""},
		{"repo only", "Autonomous agent.\n\nRepo: ahmadAlMezaal/nightshift-site", "ahmadAlMezaal/nightshift-site", ""},
		{"repo and branch", "Repo: owner/site\nBranch: staging", "owner/site", "staging"},
		{"full https url", "Repo: https://github.com/owner/site", "https://github.com/owner/site", ""},
		{"branch alone ignored", "Branch: main\nNo repo here.", "", ""},
		{"case-insensitive + spaces", "repo:   owner/x  \nBRANCH:  dev ", "owner/x", "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Project{Name: "P", Description: c.desc}
			r, b := p.RepoDirective()
			if r != c.wantRepo || b != c.wantBr {
				t.Errorf("got (%q,%q), want (%q,%q)", r, b, c.wantRepo, c.wantBr)
			}
		})
	}
}

func TestProjectRepoDirective_NilSafe(t *testing.T) {
	var p *Project
	if r, b := p.RepoDirective(); r != "" || b != "" {
		t.Errorf("nil project: got (%q,%q)", r, b)
	}
}
