package sweep

import (
	"fmt"
	"time"
)

func init() {
	Register(Task{
		Name:         "doc-drift",
		Description:  "Fix documentation that has drifted out of sync with the code",
		Cooldown:     14 * 24 * time.Hour,
		BranchSuffix: "doc-drift",
		CommitPrefix: "docs",
		PRLabel:      "maintenance",
		Prompt: func(repoPath string) string {
			return fmt.Sprintf(`You are performing autonomous maintenance on a codebase.

## Task: Documentation drift

Find and fix documentation in the project at %s that no longer matches the code.

## Instructions:
1. Read the primary docs (README, CONTRIBUTING, docs/, and any AGENTS.md / CLAUDE.md).
2. Cross-check concrete, verifiable claims against the actual code: CLI commands and flags, environment variables / config keys, package or directory layout, installation/build steps, and code examples.
3. Fix only the statements that are demonstrably wrong or stale. Update them to match the code.

## Rules:
- Documentation changes only — do NOT change code to match the docs.
- Fix only verifiable drift (a flag that was renamed, an env var that no longer exists, a wrong command). Do NOT rewrite prose for style, tone, or preference.
- Preserve the existing document structure and voice.
- If the docs accurately reflect the code, say BLOCKED: Documentation is in sync — no drift found.

## When done:
Summarize the drift you corrected (what was stale -> what it should be). Wrap the summary between these exact marker lines, each alone on its own line with nothing else:

===NOCTRA SUMMARY===
<your summary here>
===END NOCTRA SUMMARY===

RELEASE: none`, repoPath)
		},
	})
}
