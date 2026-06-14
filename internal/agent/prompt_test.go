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

func TestBuildPrompt_ReleaseInstructionWhenEnabled(t *testing.T) {
	out := BuildPrompt(BuildPromptInput{
		Identifier:       "ENG-50",
		Title:            "Add feature",
		Description:      "Details.",
		AutoReleaseLabel: true,
	})
	if !strings.Contains(out, "RELEASE: patch | minor | major | none") {
		t.Errorf("prompt should include RELEASE instruction when AutoReleaseLabel=true:\n%s", out)
	}
}

func TestBuildPrompt_NoReleaseInstructionWhenDisabled(t *testing.T) {
	out := BuildPrompt(BuildPromptInput{
		Identifier:       "ENG-51",
		Title:            "Fix bug",
		Description:      "Details.",
		AutoReleaseLabel: false,
	})
	if strings.Contains(out, "RELEASE:") {
		t.Errorf("prompt should NOT include RELEASE instruction when AutoReleaseLabel=false:\n%s", out)
	}
}

func TestBuildPrompt_ReleaseInstructionInTeamsFlavor(t *testing.T) {
	out := BuildPrompt(BuildPromptInput{
		Identifier:       "ENG-52",
		Title:            "Teams feature",
		Description:      "Details.",
		UseTeams:         true,
		AutoReleaseLabel: true,
	})
	if !strings.Contains(out, "team of agents") {
		t.Fatalf("expected the teams-flavor prompt:\n%s", out)
	}
	if !strings.Contains(out, "RELEASE: patch | minor | major | none") {
		t.Errorf("teams prompt should include RELEASE instruction when AutoReleaseLabel=true:\n%s", out)
	}
}
