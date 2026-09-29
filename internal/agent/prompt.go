package agent

import (
	"fmt"
	"strings"
)

type BuildPromptInput struct {
	Identifier       string
	Title            string
	Description      string
	Comments         []string
	UseTeams         bool
	AutoReleaseLabel bool
	RepoLessons      string
}

func RepoLessonsSection(lessons string) string {
	if strings.TrimSpace(lessons) == "" {
		return ""
	}
	return "\n\n## Repository Lessons & Conventions (from post-merge human edits to previous PRs):\n" + lessons
}

func BuildPrompt(in BuildPromptInput) string {
	desc := in.Description
	if desc == "" {
		desc = "No description provided."
	}

	discussion := ""
	if len(in.Comments) > 0 {
		discussion = "\n\n## Ticket discussion (human clarifications — treat as authoritative, newest wins):\n" +
			strings.Join(in.Comments, "\n\n")
	}

	lessonsSection := RepoLessonsSection(in.RepoLessons)

	releaseInstruction := ""
	if in.AutoReleaseLabel {
		releaseInstruction = `

After your summary (outside the markers), emit exactly one line:
RELEASE: patch | minor | major | none

Guidelines: none = docs/chore/internal-only, patch = bug fix, minor = new feature, major = breaking change.`
	}

	if in.UseTeams {
		return fmt.Sprintf(`You are a lead agent implementing a ticket. You have a team of agents available.

## Ticket: %s — %s
%s%s%s

## Your approach:
1. First, read the codebase to understand the project structure, conventions, and testing patterns.
2. Plan your implementation approach and break it into parallel tasks where possible.
3. Delegate implementation tasks to your teammates:
   - One teammate for the core implementation
   - One teammate for writing/updating tests
   - One teammate for reviewing the changes against the ticket requirements
4. Coordinate the results and ensure everything is consistent.
5. Run the full test suite and fix any failures.
6. Run the linter and fix any issues.

## Rules:
- Stay focused on this ticket only — do not modify unrelated code.
- Follow existing project conventions and patterns exactly.
- If you get stuck or need human input, say BLOCKED: <reason> and stop.
- If the ticket is already satisfied and there is genuinely nothing to change, say NO_CHANGES: <reason> and stop — do not make trivial edits just to produce a diff.
- Do NOT create PRs or push branches — Noctra handles that.

## When done:
Provide a brief summary of what was implemented and any important decisions made. Wrap the summary between these exact marker lines, each alone on its own line with nothing else:

===NOCTRA SUMMARY===
<your summary here>
===END NOCTRA SUMMARY===
%s`, in.Identifier, in.Title, desc, discussion, lessonsSection, releaseInstruction)
	}

	return fmt.Sprintf(`You are implementing a ticket.

## Ticket: %s — %s
%s%s%s

## Instructions:
1. Read the codebase to understand the project structure and conventions.
2. Implement the ticket requirements.
3. Write or update tests as needed.
4. Run the test suite and fix any failures.
5. Run the linter and fix any issues.

## Rules:
- Stay focused on this ticket only — do not modify unrelated code.
- Follow existing project conventions and patterns exactly.
- If you get stuck or need human input, say BLOCKED: <reason> and stop.
- If the ticket is already satisfied and there is genuinely nothing to change, say NO_CHANGES: <reason> and stop — do not make trivial edits just to produce a diff.
- Do NOT create PRs or push branches — Noctra handles that.

## When done:
Provide a brief summary of what was implemented and any important decisions made. Wrap the summary between these exact marker lines, each alone on its own line with nothing else:

===NOCTRA SUMMARY===
<your summary here>
===END NOCTRA SUMMARY===
%s`, in.Identifier, in.Title, desc, discussion, lessonsSection, releaseInstruction)
}
