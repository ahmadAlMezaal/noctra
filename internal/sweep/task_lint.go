package sweep

import (
	"fmt"
	"time"
)

func init() {
	Register(Task{
		Name:         "lint-cleanup",
		Description:  "Fix linter warnings and static analysis issues",
		Cooldown:     7 * 24 * time.Hour, // weekly
		BranchSuffix: "lint-cleanup",
		CommitPrefix: "chore",
		PRLabel:      "maintenance",
		Prompt: func(repoPath string) string {
			return fmt.Sprintf(`You are performing autonomous maintenance on a codebase.

## Task: Lint cleanup

Scan the codebase at %s for linter warnings and static analysis issues, then fix them.

## Instructions:
1. Read the project's linter configuration (e.g. .golangci.yml, .eslintrc, pyproject.toml) to understand what rules are enforced.
2. Run the project's linter (e.g. golangci-lint run, eslint, ruff) and capture all warnings.
3. Fix the warnings systematically — prefer minimal, targeted fixes over large refactors.
4. Re-run the linter to confirm all issues are resolved.
5. Run the test suite to make sure nothing broke.

## Rules:
- Only fix lint/static-analysis warnings. Do not refactor code or add features.
- Do not change the linter configuration to suppress warnings.
- If a warning is a false positive, add a targeted suppression comment with an explanation.
- Follow existing project conventions and patterns exactly.
- If there are no linter issues, say BLOCKED: No lint issues found — nothing to fix.
- If you cannot determine how to run the linter, say BLOCKED: Cannot determine linter setup.

## When done:
Provide a brief summary of what was fixed. Wrap the summary between these exact marker lines, each alone on its own line with nothing else:

===NOCTRA SUMMARY===
<your summary here>
===END NOCTRA SUMMARY===

RELEASE: none`, repoPath)
		},
	})
}
