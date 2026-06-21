package agent

import (
	"strings"
	"testing"
)

func TestBuildPlanPrompt_BasicFields(t *testing.T) {
	out := BuildPlanPrompt(BuildPromptInput{
		Identifier:  "ENG-221",
		Title:       "Plan-confirm step",
		Description: "Add a plan-confirm feature.",
	})

	if !strings.Contains(out, "ENG-221") {
		t.Error("prompt missing identifier")
	}
	if !strings.Contains(out, "Plan-confirm step") {
		t.Error("prompt missing title")
	}
	if !strings.Contains(out, "Add a plan-confirm feature.") {
		t.Error("prompt missing description")
	}
	if !strings.Contains(out, "Do NOT implement anything") {
		t.Error("prompt should instruct agent not to implement")
	}
	if !strings.Contains(out, PlanStartMarker) {
		t.Error("prompt missing plan start marker")
	}
	if !strings.Contains(out, PlanEndMarker) {
		t.Error("prompt missing plan end marker")
	}
}

func TestBuildPlanPrompt_IncludesComments(t *testing.T) {
	out := BuildPlanPrompt(BuildPromptInput{
		Identifier:  "ENG-221",
		Title:       "Plan-confirm step",
		Description: "Details.",
		Comments:    []string{"Ahmad: Use the existing clarification plumbing."},
	})

	if !strings.Contains(out, "Ticket discussion") {
		t.Error("prompt missing discussion section")
	}
	if !strings.Contains(out, "Use the existing clarification plumbing.") {
		t.Error("prompt missing clarification comment")
	}
}

func TestBuildPlanPrompt_NoDiscussionWhenNoComments(t *testing.T) {
	out := BuildPlanPrompt(BuildPromptInput{
		Identifier:  "ENG-1",
		Title:       "Do a thing",
		Description: "Details.",
	})
	if strings.Contains(out, "Ticket discussion") {
		t.Error("prompt should not have discussion section when no comments")
	}
}

func TestExtractPlan_Valid(t *testing.T) {
	output := `Some preamble text.

===NOCTRA PLAN===
## Summary
Modify the config to add PLAN_CONFIRM.

## Files to modify
- internal/config/config.go
===END NOCTRA PLAN===

Done.`

	plan, ok := ExtractPlan(output)
	if !ok {
		t.Fatal("expected to extract plan")
	}
	if !strings.Contains(plan, "Modify the config") {
		t.Errorf("plan missing expected content: %s", plan)
	}
	if !strings.Contains(plan, "internal/config/config.go") {
		t.Errorf("plan missing files section: %s", plan)
	}
}

func TestExtractPlan_MissingMarkers(t *testing.T) {
	_, ok := ExtractPlan("No markers here.")
	if ok {
		t.Error("expected no plan when markers are absent")
	}
}

func TestExtractPlan_EmptyBetweenMarkers(t *testing.T) {
	output := "===NOCTRA PLAN===\n   \n===END NOCTRA PLAN==="
	_, ok := ExtractPlan(output)
	if ok {
		t.Error("expected no plan when content between markers is empty")
	}
}

func TestExtractPlan_LastPairWins(t *testing.T) {
	output := `===NOCTRA PLAN===
First plan (echoed instruction)
===END NOCTRA PLAN===

===NOCTRA PLAN===
Second plan (actual)
===END NOCTRA PLAN===`

	plan, ok := ExtractPlan(output)
	if !ok {
		t.Fatal("expected to extract plan")
	}
	if !strings.Contains(plan, "Second plan") {
		t.Errorf("expected last plan to win, got: %s", plan)
	}
}

func TestBuildPlanImplementPrompt_IncludesPlan(t *testing.T) {
	plan := "1. Add config field\n2. Add agent prompt"
	out := BuildPlanImplementPrompt(BuildPromptInput{
		Identifier:  "ENG-221",
		Title:       "Plan-confirm step",
		Description: "Add a plan-confirm feature.",
	}, plan)

	if !strings.Contains(out, "Approved implementation plan") {
		t.Error("prompt missing plan section header")
	}
	if !strings.Contains(out, plan) {
		t.Error("prompt missing the plan content")
	}
	if !strings.Contains(out, SummaryStartMarker) {
		t.Error("prompt missing summary start marker")
	}
	if !strings.Contains(out, SummaryEndMarker) {
		t.Error("prompt missing summary end marker")
	}
	// Should NOT contain RELEASE instruction when AutoReleaseLabel is false.
	if strings.Contains(out, "RELEASE:") {
		t.Error("prompt should not contain RELEASE instruction when AutoReleaseLabel is false")
	}
}

func TestBuildPlanImplementPrompt_ReleaseInstruction(t *testing.T) {
	out := BuildPlanImplementPrompt(BuildPromptInput{
		Identifier:       "ENG-50",
		Title:            "Feature",
		Description:      "Details.",
		AutoReleaseLabel: true,
	}, "The plan.")

	if !strings.Contains(out, "RELEASE: patch | minor | major | none") {
		t.Error("prompt should include RELEASE instruction when AutoReleaseLabel=true")
	}
}

func TestBuildPlanImplementPrompt_IncludesComments(t *testing.T) {
	out := BuildPlanImplementPrompt(BuildPromptInput{
		Identifier:  "ENG-5",
		Title:       "Ticket",
		Description: "Details.",
		Comments:    []string{"Ahmad: prefer the stdin approach."},
	}, "The plan.")

	if !strings.Contains(out, "Ticket discussion") {
		t.Error("prompt missing discussion section")
	}
	if !strings.Contains(out, "prefer the stdin approach") {
		t.Error("prompt missing clarification comment")
	}
}
