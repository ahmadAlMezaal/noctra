package source

import (
	"context"
	"strings"
	"testing"
)

func TestJiraTicketUsesKeyAsIdentifier(t *testing.T) {
	src := NewJira(JiraConfig{
		BaseURL:        "https://test.atlassian.net",
		TriggerStatus:  "To Do",
		InReviewStatus: "In Review",
		Project:        "PROJ",
	})
	ticket := src.ticket(jiraIssue{
		ID:  "10001",
		Key: "PROJ-42",
		Fields: jiraFields{
			Summary: "Fix login flow",
			Description: &jiraADFDocument{
				Type: "doc",
				Content: []jiraADFContent{
					{Type: "paragraph", Content: []jiraADFContent{
						{Type: "text", Text: "Details about the issue"},
					}},
				},
			},
			Status:  jiraStatus{Name: "To Do"},
			Project: jiraProject{Key: "PROJ", Name: "Project"},
			Labels:  []string{"noctra", "agent:codex"},
			Comment: jiraCommentPage{Comments: []jiraComment{
				{
					Body: &jiraADFDocument{
						Type: "doc",
						Content: []jiraADFContent{
							{Type: "paragraph", Content: []jiraADFContent{
								{Type: "text", Text: "Please keep the old redirect path."},
							}},
						},
					},
					Author: jiraUser{DisplayName: "Alice"},
				},
			}},
		},
	})
	if ticket.Identifier != "PROJ-42" {
		t.Fatalf("Identifier = %q; want PROJ-42", ticket.Identifier)
	}
	if ticket.Source != "jira" {
		t.Fatalf("Source = %q; want jira", ticket.Source)
	}
	if ticket.ID != "10001" {
		t.Fatalf("ID = %q; want 10001", ticket.ID)
	}
	if ticket.URL != "https://test.atlassian.net/browse/PROJ-42" {
		t.Fatalf("URL = %q", ticket.URL)
	}
	if ticket.Title != "Fix login flow" {
		t.Fatalf("Title = %q", ticket.Title)
	}
	if ticket.Description != "Details about the issue" {
		t.Fatalf("Description = %q", ticket.Description)
	}
	if ticket.ProjectName != "PROJ" {
		t.Fatalf("ProjectName = %q", ticket.ProjectName)
	}
	if got := ticket.BackendLabel(); got != "codex" {
		t.Fatalf("BackendLabel = %q; want codex", got)
	}
	if got, want := ticket.ClarificationComments(), []string{"Alice: Please keep the old redirect path."}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("ClarificationComments = %#v; want %#v", got, want)
	}
}

func TestJiraTicketHonorsRepoDirective(t *testing.T) {
	src := NewJira(JiraConfig{
		BaseURL:        "https://test.atlassian.net",
		TriggerStatus:  "To Do",
		InReviewStatus: "In Review",
		Project:        "PROJ",
	})
	ticket := src.ticket(jiraIssue{
		ID:  "10002",
		Key: "PROJ-7",
		Fields: jiraFields{
			Summary: "Route elsewhere",
			Description: &jiraADFDocument{
				Type: "doc",
				Content: []jiraADFContent{
					{Type: "paragraph", Content: []jiraADFContent{
						{Type: "text", Text: "Repo: acme/api\nBranch: develop"},
					}},
				},
			},
			Status:  jiraStatus{Name: "To Do"},
			Project: jiraProject{Key: "PROJ"},
		},
	})
	if ticket.RepoRef != "acme/api" || ticket.RepoBranch != "develop" {
		t.Fatalf("repo directive = %q, %q; want acme/api, develop", ticket.RepoRef, ticket.RepoBranch)
	}
}

func TestJiraTicketNoDescription(t *testing.T) {
	src := NewJira(JiraConfig{
		BaseURL:        "https://test.atlassian.net",
		TriggerStatus:  "To Do",
		InReviewStatus: "In Review",
		Project:        "PROJ",
	})
	ticket := src.ticket(jiraIssue{
		ID:  "10003",
		Key: "PROJ-99",
		Fields: jiraFields{
			Summary:     "No body",
			Description: nil,
			Status:      jiraStatus{Name: "To Do"},
			Project:     jiraProject{Key: "PROJ"},
		},
	})
	if ticket.Description != "" {
		t.Fatalf("Description = %q; want empty", ticket.Description)
	}
	if ticket.RepoRef != "" {
		t.Fatalf("RepoRef = %q; want empty", ticket.RepoRef)
	}
}

func TestJiraADFToText(t *testing.T) {
	doc := &jiraADFDocument{
		Type: "doc",
		Content: []jiraADFContent{
			{Type: "paragraph", Content: []jiraADFContent{
				{Type: "text", Text: "Hello "},
				{Type: "text", Text: "world"},
			}},
			{Type: "paragraph", Content: []jiraADFContent{
				{Type: "text", Text: "Second paragraph"},
			}},
		},
	}
	got := adfToText(doc)
	want := "Hello world\nSecond paragraph"
	if got != want {
		t.Fatalf("adfToText = %q; want %q", got, want)
	}
}

func TestJiraADFToTextNil(t *testing.T) {
	if got := adfToText(nil); got != "" {
		t.Fatalf("adfToText(nil) = %q; want empty", got)
	}
}

func TestJiraADFToTextRich(t *testing.T) {
	doc := &jiraADFDocument{
		Type: "doc",
		Content: []jiraADFContent{
			{Type: "paragraph", Content: []jiraADFContent{
				{Type: "text", Text: "Line 1"},
				{Type: "hardBreak"},
				{Type: "text", Text: "Line 2"},
			}},
			{Type: "bulletList", Content: []jiraADFContent{
				{Type: "listItem", Content: []jiraADFContent{
					{Type: "paragraph", Content: []jiraADFContent{
						{Type: "text", Text: "Item 1"},
					}},
				}},
				{Type: "listItem", Content: []jiraADFContent{
					{Type: "paragraph", Content: []jiraADFContent{
						{Type: "text", Text: "Item 2"},
					}},
				}},
			}},
		},
	}
	got := adfToText(doc)
	want := "Line 1\nLine 2\nItem 1\nItem 2"
	if got != want {
		t.Fatalf("adfToText rich = %q; want %q", got, want)
	}
}

func TestJiraADFToTextRepoDirectiveSeparateParagraphs(t *testing.T) {
	// Repo: and Branch: in separate paragraphs must yield separate lines so ParseRepoDirective finds both.
	doc := &jiraADFDocument{
		Type: "doc",
		Content: []jiraADFContent{
			{Type: "paragraph", Content: []jiraADFContent{
				{Type: "text", Text: "Repo: acme/api"},
			}},
			{Type: "paragraph", Content: []jiraADFContent{
				{Type: "text", Text: "Branch: develop"},
			}},
		},
	}
	got := adfToText(doc)
	if !strings.Contains(got, "Repo: acme/api") {
		t.Fatalf("missing Repo directive in %q", got)
	}
	ref, branch := ParseRepoDirective(got)
	if ref != "acme/api" || branch != "develop" {
		t.Fatalf("ParseRepoDirective(%q) = %q, %q; want acme/api, develop", got, ref, branch)
	}
}

func TestJiraBuildFetchJQLStatus(t *testing.T) {
	src := NewJira(JiraConfig{
		Project:       "PROJ",
		TriggerStatus: "To Do",
	})
	got := src.buildFetchJQL()
	want := `project = "PROJ" AND status = "To Do" ORDER BY created ASC`
	if got != want {
		t.Fatalf("buildFetchJQL (status) = %q; want %q", got, want)
	}
}

func TestJiraBuildFetchJQLLabel(t *testing.T) {
	src := NewJira(JiraConfig{
		Project:       "PROJ",
		TriggerLabel:  "noctra",
		TriggerStatus: "To Do",
	})
	got := src.buildFetchJQL()
	want := `project = "PROJ" AND labels = "noctra" AND statusCategory != "Done" ORDER BY created ASC`
	if got != want {
		t.Fatalf("buildFetchJQL (label) = %q; want %q", got, want)
	}
}

func TestJiraPrepareValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  JiraConfig
		want string
	}{
		{
			name: "missing base URL",
			cfg:  JiraConfig{UserEmail: "x", APIToken: "x", Project: "X", TriggerStatus: "X", InReviewStatus: "X"},
			want: "JIRA_BASE_URL",
		},
		{
			name: "missing user email",
			cfg:  JiraConfig{BaseURL: "https://x.atlassian.net", APIToken: "x", Project: "X", TriggerStatus: "X", InReviewStatus: "X"},
			want: "JIRA_USER_EMAIL",
		},
		{
			name: "missing API token",
			cfg:  JiraConfig{BaseURL: "https://x.atlassian.net", UserEmail: "x", Project: "X", TriggerStatus: "X", InReviewStatus: "X"},
			want: "JIRA_API_TOKEN",
		},
		{
			name: "missing project",
			cfg:  JiraConfig{BaseURL: "https://x.atlassian.net", UserEmail: "x", APIToken: "x", TriggerStatus: "X", InReviewStatus: "X"},
			want: "JIRA_PROJECT",
		},
		{
			name: "missing trigger",
			cfg:  JiraConfig{BaseURL: "https://x.atlassian.net", UserEmail: "x", APIToken: "x", Project: "X", InReviewStatus: "X"},
			want: "JIRA_TRIGGER_STATUS",
		},
		{
			name: "missing in-review status",
			cfg:  JiraConfig{BaseURL: "https://x.atlassian.net", UserEmail: "x", APIToken: "x", Project: "X", TriggerStatus: "X"},
			want: "JIRA_IN_REVIEW_STATUS",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := NewJira(tt.cfg)
			err := src.Prepare(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Fatalf("error = %q; want to contain %q", got, tt.want)
			}
		})
	}
}

func TestJiraPrepareTrimsBaseURL(t *testing.T) {
	src := NewJira(JiraConfig{
		BaseURL:        "https://test.atlassian.net/",
		UserEmail:      "x@x.com",
		APIToken:       "token",
		Project:        "P",
		TriggerStatus:  "Open",
		InReviewStatus: "In Review",
	})
	if err := src.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.cfg.BaseURL != "https://test.atlassian.net" {
		t.Fatalf("BaseURL = %q; trailing slash not trimmed", src.cfg.BaseURL)
	}
}

func TestJiraPrepareLabelModeSatisfiesTrigger(t *testing.T) {
	src := NewJira(JiraConfig{
		BaseURL:        "https://test.atlassian.net",
		UserEmail:      "x@x.com",
		APIToken:       "token",
		Project:        "P",
		TriggerLabel:   "noctra",
		InReviewStatus: "In Review",
	})
	if err := src.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() unexpected error: %v", err)
	}
}
