package source

import "testing"

func TestGitHubIssuesTicketUsesIssueRepoByDefault(t *testing.T) {
	src := NewGitHubIssues(GitHubIssuesConfig{TriggerLabel: "noctra"})
	ticket := src.ticket("acme/widgets", githubIssue{
		Number: 42,
		Title:  "Fix login",
		Body:   "Details",
		URL:    "https://github.com/acme/widgets/issues/42",
		Labels: []githubLabel{{Name: "noctra"}, {Name: "agent:codex"}},
	})
	if ticket.Identifier != "GH-ACME-WIDGETS-42" {
		t.Fatalf("Identifier = %q", ticket.Identifier)
	}
	if ticket.RepoRef != "acme/widgets" {
		t.Fatalf("RepoRef = %q; want issue repo", ticket.RepoRef)
	}
	if got := ticket.BackendLabel(); got != "codex" {
		t.Fatalf("BackendLabel = %q; want codex", got)
	}
}

func TestGitHubIssuesTicketHonorsRepoDirective(t *testing.T) {
	src := NewGitHubIssues(GitHubIssuesConfig{TriggerLabel: "noctra"})
	ticket := src.ticket("acme/inbox", githubIssue{
		Number: 7,
		Title:  "Route elsewhere",
		Body:   "Repo: acme/api\nBranch: develop",
		URL:    "https://github.com/acme/inbox/issues/7",
	})
	if ticket.RepoRef != "acme/api" || ticket.RepoBranch != "develop" {
		t.Fatalf("repo directive = %q, %q; want acme/api, develop", ticket.RepoRef, ticket.RepoBranch)
	}
}
