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

func TestBackendLabel(t *testing.T) {
	cases := []struct {
		name   string
		labels []Label
		want   string
	}{
		{"no labels", nil, ""},
		{"unrelated labels", []Label{{Name: "bug"}, {Name: "noctra"}}, ""},
		{"agent:codex", []Label{{Name: "agent:codex"}}, "codex"},
		{"agent:claude", []Label{{Name: "priority"}, {Name: "agent:claude"}}, "claude"},
		{"agent:copilot", []Label{{Name: "agent:copilot"}}, "copilot"},
		// Case-insensitive + trimmed.
		{"Agent:Codex", []Label{{Name: "Agent:Codex"}}, "codex"},
		{"spaces", []Label{{Name: " agent: claude "}}, "claude"},
		// Empty suffix ignored.
		{"agent: (empty)", []Label{{Name: "agent:"}}, ""},
		{"agent: (spaces)", []Label{{Name: "agent:   "}}, ""},
		// First match wins.
		{"first wins", []Label{{Name: "agent:codex"}, {Name: "agent:claude"}}, "codex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issue := Issue{Labels: LabelConnection{Nodes: c.labels}}
			if got := issue.BackendLabel(); got != c.want {
				t.Errorf("BackendLabel() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBackendLabel_EmptyIssue(t *testing.T) {
	if got := (Issue{}).BackendLabel(); got != "" {
		t.Errorf("empty issue BackendLabel() = %q, want empty", got)
	}
}

func TestHasLabel(t *testing.T) {
	cases := []struct {
		name   string
		labels []Label
		target string
		want   bool
	}{
		{"no labels", nil, "plan-first", false},
		{"exact match", []Label{{Name: "plan-first"}}, "plan-first", true},
		{"case-insensitive", []Label{{Name: "Plan-First"}}, "plan-first", true},
		{"whitespace trimmed", []Label{{Name: " plan-first "}}, "plan-first", true},
		{"no match", []Label{{Name: "bug"}, {Name: "noctra"}}, "plan-first", false},
		{"empty target", []Label{{Name: "bug"}}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issue := Issue{Labels: LabelConnection{Nodes: c.labels}}
			if got := issue.HasLabel(c.target); got != c.want {
				t.Errorf("HasLabel(%q) = %v, want %v", c.target, got, c.want)
			}
		})
	}
}

func TestIsApprovalComment(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"go", true},
		{"Go", true},
		{"GO", true},
		{"  go  ", true},
		{"lgtm", true},
		{"LGTM", true},
		{"approved", true},
		{"approve", true},
		{"👍", true},
		{":thumbsup:", true},
		{":+1:", true},
		// Non-approval comments.
		{"", false},
		{"looks good but needs changes", false},
		{"go ahead and fix the bug too", false},
		{"not approved", false},
		{"please go fix it", false},
	}
	for _, c := range cases {
		t.Run(c.body, func(t *testing.T) {
			if got := IsApprovalComment(c.body); got != c.want {
				t.Errorf("IsApprovalComment(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestIsSystemComment_PlanConfirmComment(t *testing.T) {
	planComment := PlanConfirmCommentPrefix + "\n\n## Summary\nDo this and that."
	if !IsSystemComment(planComment) {
		t.Error("plan-confirm comment should be classified as a system comment")
	}
}
