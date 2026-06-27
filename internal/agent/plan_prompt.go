package agent

import (
	"fmt"
	"strings"
)

// BuildPlanPrompt returns a plan-only prompt: the agent produces an implementation plan (posted to Linear for human review) instead of implementing.
func BuildPlanPrompt(in BuildPromptInput) string {
	desc := in.Description
	if desc == "" {
		desc = "No description provided."
	}

	discussion := ""
	if len(in.Comments) > 0 {
		discussion = "\n\n## Ticket discussion (human clarifications — treat as authoritative, newest wins):\n" +
			strings.Join(in.Comments, "\n\n")
	}

	return fmt.Sprintf(`You are reviewing a Linear ticket and producing an implementation plan. Do NOT implement anything — only plan.

## Ticket: %s — %s
%s%s

## Instructions:
1. Read the codebase to understand the project structure, conventions, and relevant code.
2. Identify the files that need to be created or modified.
3. Outline the implementation steps in detail.
4. Note any risks, open questions, or decisions that need human input.
5. Do NOT write any code, create files, run tests, or make any changes to the repository.

## Output format:
Produce your plan between these exact marker lines, each alone on its own line with nothing else:

===NOCTRA PLAN===
<your detailed implementation plan here>
===END NOCTRA PLAN===

The plan should include:
- **Summary**: A brief overview of the approach.
- **Files to modify**: List each file and what changes are needed.
- **New files**: Any new files to create and their purpose.
- **Implementation steps**: Ordered steps to implement the feature.
- **Testing strategy**: How to test the changes.
- **Risks / open questions**: Anything that might need clarification.`, in.Identifier, in.Title, desc, discussion)
}

// Plan markers the plan-only prompt asks the agent to wrap its plan in.
const (
	PlanStartMarker = "===NOCTRA PLAN==="
	PlanEndMarker   = "===END NOCTRA PLAN==="
)

// ExtractPlan returns the plan between the Plan markers; ("", false) when absent or empty.
func ExtractPlan(output string) (string, bool) {
	start := strings.LastIndex(output, PlanStartMarker)
	if start < 0 {
		return "", false
	}
	rest := output[start+len(PlanStartMarker):]
	end := strings.Index(rest, PlanEndMarker)
	if end < 0 {
		return "", false
	}
	plan := strings.TrimSpace(rest[:end])
	if plan == "" {
		return "", false
	}
	return plan, true
}

// BuildPlanImplementPrompt returns the implementation prompt carrying the human-approved plan as context.
func BuildPlanImplementPrompt(in BuildPromptInput, plan string) string {
	desc := in.Description
	if desc == "" {
		desc = "No description provided."
	}

	discussion := ""
	if len(in.Comments) > 0 {
		discussion = "\n\n## Ticket discussion (human clarifications — treat as authoritative, newest wins):\n" +
			strings.Join(in.Comments, "\n\n")
	}

	releaseInstruction := ""
	if in.AutoReleaseLabel {
		releaseInstruction = `

After your summary (outside the markers), emit exactly one line:
RELEASE: patch | minor | major | none

Guidelines: none = docs/chore/internal-only, patch = bug fix, minor = new feature, major = breaking change.`
	}

	return fmt.Sprintf(`You are implementing a Linear ticket. A plan was previously created and approved by a human reviewer. Follow the plan closely, but adapt if you discover issues during implementation.

## Ticket: %s — %s
%s%s

## Approved implementation plan:
%s

## Instructions:
1. Follow the approved plan above as closely as possible.
2. Implement the ticket requirements according to the plan.
3. Write or update tests as needed.
4. Run the test suite and fix any failures.
5. Run the linter and fix any issues.
6. If you need to deviate from the plan, explain why in your summary.

## Rules:
- Stay focused on this ticket only — do not modify unrelated code.
- Follow existing project conventions and patterns exactly.
- If you get stuck or need human input, say BLOCKED: <reason> and stop.
- Do NOT create PRs or push branches — Noctra handles that.

## When done:
Provide a brief summary of what was implemented and any important decisions made. Wrap the summary between these exact marker lines, each alone on its own line with nothing else:

===NOCTRA SUMMARY===
<your summary here>
===END NOCTRA SUMMARY===
%s`, in.Identifier, in.Title, desc, discussion, plan, releaseInstruction)
}
