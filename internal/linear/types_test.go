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
			{Body: "Use https://getnoctra.dev as the canonical URL.", User: &User{Name: "Ahmad"}},
			// Noctra's own BLOCKED notification — filtered (would otherwise be
			// echoed back at the agent).
			{Body: "🚧 **Noctra needs your input** (attempt 1/3)\n\nThe agent got blocked..."},
			// Legacy pre-rename (Nightshift) automated notice — still filtered so a
			// re-dispatched older ticket doesn't echo it back to the agent (ENG-204).
			{Body: "🚧 **Nightshift needs your input** (attempt 2/3)\n\nThe agent got blocked..."},
			// Whitespace-only — skipped.
			{Body: "   "},
			// Author missing — kept, attributed to "Someone".
			{Body: "Also add a badge near the title."},
			// Human reply that QUOTES the bot notification, then adds their own
			// clarification — must be kept, not mistaken for a system comment.
			{Body: "> 🚧 **Noctra needs your input**\n\nActually, use the other URL.", User: &User{Name: "Ahmad"}},
		}},
	}

	got := issue.ClarificationComments()
	want := []string{
		"Ahmad: Use https://getnoctra.dev as the canonical URL.",
		"Someone: Also add a badge near the title.",
		"Ahmad: > 🚧 **Noctra needs your input**\n\nActually, use the other URL.",
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
		content, desc    string // content = markdown body (primary), desc = short description (fallback)
		wantRepo, wantBr string
	}{
		{"none", "", "Just a normal project summary.", "", ""},
		// Directive in the markdown body (content) — the real-world case that
		// the ENG-200 bug broke (we used to read description only).
		{"content repo only", "Autonomous agent.\n\nRepo: ahmadAlMezaal/noctra-site", "", "ahmadAlMezaal/noctra-site", ""},
		{"content repo + branch", "Repo: owner/site\nBranch: staging", "", "owner/site", "staging"},
		{"content full https url", "Repo: https://github.com/owner/site", "", "https://github.com/owner/site", ""},
		// Fallback: directive written in the short description still works.
		{"description fallback", "", "Repo: owner/x", "owner/x", ""},
		// content takes precedence over description.
		{"content beats description", "Repo: owner/from-content", "Repo: owner/from-desc", "owner/from-content", ""},
		{"branch alone ignored", "Branch: main\nNo repo here.", "", "", ""},
		{"case-insensitive + spaces", "repo:   owner/x  \nBRANCH:  dev ", "", "owner/x", "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Project{Name: "P", Content: c.content, Description: c.desc}
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
