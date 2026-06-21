package source

import (
	"reflect"
	"testing"
)

func TestParseRepoDirective(t *testing.T) {
	repo, branch := ParseRepoDirective("notes", "Repo: acme/widgets\nBranch: release")
	if repo != "acme/widgets" || branch != "release" {
		t.Fatalf("ParseRepoDirective() = %q, %q; want acme/widgets, release", repo, branch)
	}
}

func TestTicketClarificationCommentsFiltersSystemComments(t *testing.T) {
	ticket := Ticket{
		Comments: []Comment{
			{Author: "noctra", Body: "🌙 **Noctra created a PR**"},
			{Author: "Ahmad", Body: "Please use the existing helper."},
			{Author: "Reviewer", Body: "> 🌙 **Noctra created a PR**\n\nThis still needs tests."},
		},
	}
	got := ticket.ClarificationComments()
	want := []string{
		"Ahmad: Please use the existing helper.",
		"Reviewer: > 🌙 **Noctra created a PR**\n\nThis still needs tests.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClarificationComments() = %#v; want %#v", got, want)
	}
}

func TestTicketBackendLabel(t *testing.T) {
	ticket := Ticket{Labels: []Label{{Name: "bug"}, {Name: "agent:codex"}}}
	if got := ticket.BackendLabel(); got != "codex" {
		t.Fatalf("BackendLabel() = %q; want codex", got)
	}
}
