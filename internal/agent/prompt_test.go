package agent

import (
	"strings"
	"testing"
)

func TestBuildPrompt_IncludesComments(t *testing.T) {
	out := BuildPrompt(BuildPromptInput{
		Identifier:  "ENG-186",
		Title:       "Add website URL",
		Description: "Add the URL to the README.",
		Comments: []string{
			"Ahmad: Use https://getnoctra.dev as the canonical URL.",
		},
	})

	if !strings.Contains(out, "## Ticket discussion") {
		t.Errorf("prompt missing discussion section:\n%s", out)
	}
	if !strings.Contains(out, "https://getnoctra.dev") {
		t.Errorf("prompt missing the clarification comment:\n%s", out)
	}
}

func TestBuildPrompt_NoDiscussionSectionWhenNoComments(t *testing.T) {
	out := BuildPrompt(BuildPromptInput{
		Identifier:  "ENG-1",
		Title:       "Do a thing",
		Description: "Details.",
	})
	if strings.Contains(out, "Ticket discussion") {
		t.Errorf("prompt should not have a discussion section when there are no comments:\n%s", out)
	}
}

func TestBuildPrompt_CommentsInTeamsFlavor(t *testing.T) {
	out := BuildPrompt(BuildPromptInput{
		Identifier:  "ENG-2",
		Title:       "Teams ticket",
		Description: "Details.",
		Comments:    []string{"Ahmad: prefer the stdin approach."},
		UseTeams:    true,
	})
	if !strings.Contains(out, "team of agents") {
		t.Fatalf("expected the teams-flavor prompt:\n%s", out)
	}
	if !strings.Contains(out, "prefer the stdin approach") {
		t.Errorf("teams prompt missing the clarification comment:\n%s", out)
	}
}
