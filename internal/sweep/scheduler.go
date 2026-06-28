package sweep

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

// RepoResolver is the subset of *repo.Resolver the scheduler needs: discover cloned repos, or resolve an explicit one (cloning on demand).
type RepoResolver interface {
	AllRepoPaths() []string
	ResolveDirect(ctx context.Context, ref, branch string) (repo.Resolved, error)
}

// Scheduler decides when the next sweep fires and which tasks are eligible on which repos; side-effect-free (the pipeline executes).
type Scheduler struct {
	store      *state.Store
	resolver   RepoResolver
	tasks      []Task
	interval   time.Duration
	maxTasks   int
	schedule   *CronSchedule
	sweepRepos []string

	lastSweep     time.Time
	startedAt     time.Time
	now           func() time.Time
	lastRef       time.Time
	nextScheduled time.Time
	repoRotation  int
}

func NewScheduler(store *state.Store, resolver RepoResolver, tasks []Task, interval time.Duration, maxTasks int, schedule *CronSchedule, sweepRepos []string) *Scheduler {
	now := time.Now
	return &Scheduler{
		store:      store,
		resolver:   resolver,
		tasks:      tasks,
		interval:   interval,
		maxTasks:   maxTasks,
		schedule:   schedule,
		sweepRepos: sweepRepos,
		lastSweep:  time.Time{},
		startedAt:  now(),
		now:        now,
	}
}

// Job is one eligible (repo, task) pair ready to be dispatched.
type Job struct {
	Task       Task
	RepoPath   string // local clone path
	RepoSlug   string // slug for branch/identifier naming
	MainBranch string
}

// DueIn returns time until the next sweep (0 if due now). Cron waits for the next matching time; interval fires immediately on startup (lastSweep is zero).
func (s *Scheduler) DueIn() time.Duration {
	now := s.now()
	if s.schedule != nil {
		ref := s.lastSweep
		if ref.IsZero() {
			ref = s.startedAt
		}
		if !ref.Equal(s.lastRef) {
			s.lastRef = ref
			s.nextScheduled = s.schedule.Next(ref)
		}
		if s.nextScheduled.IsZero() {
			return s.intervalDueIn(now)
		}
		if d := s.nextScheduled.Sub(now); d > 0 {
			return d
		}
		return 0
	}
	return s.intervalDueIn(now)
}

func (s *Scheduler) intervalDueIn(now time.Time) time.Duration {
	elapsed := now.Sub(s.lastSweep)
	if elapsed >= s.interval {
		return 0
	}
	return s.interval - elapsed
}

// MarkSwept records that a sweep cycle just completed.
func (s *Scheduler) MarkSwept() {
	s.lastSweep = s.now()
}

// repoPaths returns the explicit SWEEP_REPOS list (resolved/cloned on demand) when set, else every cloned repo.
func (s *Scheduler) repoPaths(ctx context.Context) []string {
	if len(s.sweepRepos) == 0 {
		return s.resolver.AllRepoPaths()
	}
	var paths []string
	seen := make(map[string]bool)
	for _, ref := range s.sweepRepos {
		res, err := s.resolver.ResolveDirect(ctx, ref, "")
		if err != nil {
			slog.Warn("sweep: skipping repo it could not resolve", "repo", ref, "err", err)
			continue
		}
		if seen[res.Path] {
			continue
		}
		seen[res.Path] = true
		paths = append(paths, res.Path)
	}
	return paths
}

// Plan returns eligible (repo, task) jobs (≤ maxTasks); a task is eligible once its per-repo cooldown has expired.
func (s *Scheduler) Plan(ctx context.Context) []Job {
	repoPaths := s.repoPaths(ctx)
	if len(repoPaths) == 0 {
		slog.Debug("sweep: no repos to sweep")
		return nil
	}

	var groups [][]Job
	for _, rp := range repoPaths {
		if ctx.Err() != nil {
			break
		}
		slug := repo.SlugFromPath(rp)
		if slug == "" {
			continue
		}
		var eligible []Task
		for _, task := range s.tasks {
			key := state.SweepKey(slug, task.Name)
			ss := s.store.GetSweep(key)
			if !ss.LastRunAt.IsZero() && s.now().Sub(ss.LastRunAt) < task.Cooldown {
				slog.Debug("sweep: task on cooldown",
					"task", task.Name, "repo", slug,
					"last_run", ss.LastRunAt,
					"cooldown_remaining", task.Cooldown-s.now().Sub(ss.LastRunAt))
				continue
			}
			eligible = append(eligible, task)
		}
		if len(eligible) == 0 {
			continue
		}
		mainBranch := repo.DefaultBranchOf(ctx, rp)
		repoJobs := make([]Job, len(eligible))
		for i, task := range eligible {
			repoJobs[i] = Job{Task: task, RepoPath: rp, RepoSlug: slug, MainBranch: mainBranch}
		}
		groups = append(groups, repoJobs)
	}

	if n := len(groups); n > 0 {
		off := s.repoRotation % n
		groups = append(groups[off:], groups[:off]...)
		s.repoRotation++
	}

	jobs := roundRobin(groups, s.maxTasks)
	slog.Info("sweep plan",
		"repos", len(repoPaths),
		"tasks", len(s.tasks),
		"eligible", len(jobs),
		"max", s.maxTasks)
	return jobs
}

// roundRobin interleaves groups one-per-pass, ≤ limit items, so the budget spreads across groups; preserves intra-group order, doesn't mutate inputs.
func roundRobin[T any](groups [][]T, limit int) []T {
	if limit <= 0 {
		return nil
	}
	var out []T
	for pass := 0; len(out) < limit; pass++ {
		progressed := false
		for _, g := range groups {
			if len(out) >= limit {
				break
			}
			if pass < len(g) {
				out = append(out, g[pass])
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return out
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
