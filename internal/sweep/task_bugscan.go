package sweep

import (
	"fmt"
	"time"
)

func init() {
	Register(Task{
		Name:         "bug-scan",
		Description:  "Find and fix medium-to-high-confidence bugs (error handling, nil-deref, leaks)",
		Cooldown:     14 * 24 * time.Hour,
		BranchSuffix: "bug-scan",
		CommitPrefix: "fix",
		PRLabel:      "maintenance",
		Prompt: func(repoPath string) string {
			return fmt.Sprintf(`You are performing autonomous maintenance on a codebase.

## Task: Medium-to-high-confidence bug scan

Scan the project at %s for real bugs you have medium-to-high confidence in, and fix those. This is not a refactor and not a style pass.

## In scope (fix these when you have at least medium confidence they are real defects):
- Unchecked errors that can cause silent data loss or wrong behaviour.
- Nil-pointer / nil-map dereferences and missing nil checks on values that can be nil.
- Resource leaks: unclosed files, HTTP bodies, DB rows, contexts; missing defer Close.
- Obvious logic errors: inverted conditions, off-by-one, wrong variable used, unreachable code with side effects.
- Concurrency: data races, a mutex locked but never unlocked on a path, goroutine leaks.

## Out of scope (do NOT touch):
- Style, naming, formatting, comments, performance micro-optimizations.
- Anything that changes intended behaviour or public APIs.
- Speculative "this could be better" changes, or low-confidence guesses about a possible bug.

## Instructions:
1. Read the code and any available static-analysis output (go vet, staticcheck) to locate candidate defects.
2. For each candidate, confirm it is a genuine bug before changing anything. Act on medium-to-high-confidence defects; skip only the low-confidence ones.
3. Apply the smallest correct fix. Add or adjust a test that demonstrates the bug is fixed where practical.
4. Run the build and full test suite; everything must pass.

## Rules:
- Quality over quantity. A few real, well-justified fixes are the goal; zero is an acceptable outcome.
- Every fix must be one you can clearly justify as a real defect — medium-to-high confidence, never a guess.
- If you do not find a bug of at least medium confidence, say BLOCKED: No medium-or-higher-confidence bugs found.

## When done:
For each fix, state the bug, why it is a bug, and the fix. Wrap the summary between these exact marker lines, each alone on its own line with nothing else:

===NOCTRA SUMMARY===
<your summary here>
===END NOCTRA SUMMARY===

RELEASE: none`, repoPath)
		},
	})
}
