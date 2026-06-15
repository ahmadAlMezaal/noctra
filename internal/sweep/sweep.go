// Package sweep implements autonomous maintenance sweeps for Noctra (ENG-222).
// A scheduled sweep scans every known repo, selects eligible tasks from the
// task catalog (respecting per-task cooldowns), runs the coding agent with a
// task-specific prompt, and opens a PR for each change — all under the same
// budget caps and worker pool as ticket-driven work.
//
// Fully opt-in via SWEEP_ENABLED=true in .env; off by default.
package sweep

import (
	"strings"
	"time"
)

// Task describes a maintenance task in the catalog.
type Task struct {
	// Name is the unique identifier (e.g. "lint-cleanup", "dead-code").
	Name string
	// Description is a short human-readable summary shown in logs and PR bodies.
	Description string
	// Cooldown is the minimum duration between runs of this task on the same
	// repo. Prevents the same sweep from re-running nightly.
	Cooldown time.Duration
	// Prompt returns the full agent prompt for this task. repoPath is the
	// local checkout path so prompts can reference it.
	Prompt func(repoPath string) string
	// BranchSuffix is appended to the sweep branch identity (e.g.
	// "lint-cleanup" → "noctra/sweep-<repo>-lint-cleanup"). Must be unique
	// across tasks.
	BranchSuffix string
	// CommitPrefix is the conventional-commit prefix (e.g. "chore", "fix").
	CommitPrefix string
	// PRLabel is an optional GitHub label applied to the PR (e.g. "maintenance").
	PRLabel string
}

// catalog is the built-in set of maintenance tasks, registered at init time.
var catalog []Task

// Register adds a task to the global catalog. Called from task_*.go init()
// functions.
func Register(t Task) {
	catalog = append(catalog, t)
}

// Catalog returns a copy of all registered tasks.
func Catalog() []Task {
	out := make([]Task, len(catalog))
	copy(out, catalog)
	return out
}

// FilterTasks returns the subset of catalog tasks whose names appear in
// enabled. If enabled is nil/empty, all tasks are returned (default: all).
func FilterTasks(enabled []string) []Task {
	if len(enabled) == 0 {
		return Catalog()
	}
	set := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		set[n] = true
	}
	var out []Task
	for _, t := range catalog {
		if set[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// SweepBranchName returns the branch Noctra creates for a sweep task on a
// repo (e.g. "noctra/sweep-myrepo-lint-cleanup"). This is distinct from the
// ticket-driven "noctra/<identifier>" and includes the repo slug so the
// auto-iterate watcher can reconstruct the same identifier.
func SweepBranchName(repoSlug, taskSuffix string) string {
	return "noctra/" + strings.ToLower(SweepIdentifier(repoSlug, taskSuffix))
}

// SweepIdentifier returns the worktree identifier for a sweep task on a
// repo slug (e.g. "SWEEP-MYREPO-LINT-CLEANUP"). Used as the worktree
// directory name and the key for the active-set dedup.
func SweepIdentifier(repoSlug, taskSuffix string) string {
	return strings.ToUpper("SWEEP-" + repoSlug + "-" + taskSuffix)
}
