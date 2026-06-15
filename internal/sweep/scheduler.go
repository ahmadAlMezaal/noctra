package sweep

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

// Scheduler determines when the next sweep should fire and which tasks are
// eligible on which repos. It is side-effect-free: the pipeline drives
// actual execution.
type Scheduler struct {
	store    *state.Store
	resolver *repo.Resolver
	tasks    []Task
	interval time.Duration
	maxTasks int

	// lastSweep tracks when the last sweep cycle completed. Zero-valued on
	// startup so an immediate sweep fires if tasks are due (per-task cooldowns
	// in the state store prevent repeated runs).
	lastSweep time.Time
	// now is a hook for testing — defaults to time.Now.
	now func() time.Time
}

// NewScheduler creates a sweep scheduler.
func NewScheduler(store *state.Store, resolver *repo.Resolver, tasks []Task, interval time.Duration, maxTasks int) *Scheduler {
	return &Scheduler{
		store:     store,
		resolver:  resolver,
		tasks:     tasks,
		interval:  interval,
		maxTasks:  maxTasks,
		lastSweep: time.Time{}, // allow immediate sweep on startup if tasks are due (cooldowns prevent spam)
		now:       time.Now,
	}
}

// Job is one eligible (repo, task) pair ready to be dispatched.
type Job struct {
	Task       Task
	RepoPath   string // local clone path
	RepoSlug   string // slug for branch/identifier naming
	MainBranch string // base branch
}

// DueIn returns how long until the next sweep should fire. Returns 0 if
// a sweep is already due.
func (s *Scheduler) DueIn() time.Duration {
	elapsed := s.now().Sub(s.lastSweep)
	if elapsed >= s.interval {
		return 0
	}
	return s.interval - elapsed
}

// MarkSwept records that a sweep cycle just completed.
func (s *Scheduler) MarkSwept() {
	s.lastSweep = s.now()
}

// Plan scans all known repos and returns the list of eligible (repo, task)
// jobs — at most maxTasks total. A task is eligible if its cooldown on the
// repo has expired.
func (s *Scheduler) Plan(ctx context.Context) []Job {
	repoPaths := s.resolver.AllRepoPaths()
	if len(repoPaths) == 0 {
		slog.Debug("sweep: no repos cloned yet")
		return nil
	}

	var jobs []Job
	for _, rp := range repoPaths {
		if ctx.Err() != nil {
			break
		}
		slug := repo.SlugFromPath(rp)
		if slug == "" {
			continue
		}
		mainBranch := repo.DefaultBranchOf(ctx, rp)

		for _, task := range s.tasks {
			if len(jobs) >= s.maxTasks {
				break
			}
			key := state.SweepKey(slug, task.Name)
			ss := s.store.GetSweep(key)
			if !ss.LastRunAt.IsZero() && s.now().Sub(ss.LastRunAt) < task.Cooldown {
				slog.Debug("sweep: task on cooldown",
					"task", task.Name, "repo", slug,
					"last_run", ss.LastRunAt,
					"cooldown_remaining", task.Cooldown-s.now().Sub(ss.LastRunAt))
				continue
			}
			jobs = append(jobs, Job{
				Task:       task,
				RepoPath:   rp,
				RepoSlug:   slug,
				MainBranch: mainBranch,
			})
		}
		if len(jobs) >= s.maxTasks {
			break
		}
	}

	slog.Info("sweep plan",
		"repos", len(repoPaths),
		"tasks", len(s.tasks),
		"eligible", len(jobs),
		"max", s.maxTasks)
	return jobs
}

// RecordRun persists that a task just ran on a repo.
func (s *Scheduler) RecordRun(repoSlug, taskName string) error {
	key := state.SweepKey(repoSlug, taskName)
	return s.store.UpdateSweep(key, func(ss *state.SweepState) {
		ss.LastRunAt = s.now()
	})
}

// Summary returns a human-readable status of sweep task cooldowns.
func (s *Scheduler) Summary() string {
	var out string
	for _, t := range s.tasks {
		out += fmt.Sprintf("  - %s (cooldown: %s)\n", t.Name, t.Cooldown)
	}
	if out == "" {
		return "  (no tasks registered)"
	}
	return out
}
