package sweep

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onelastcommit/noctra/internal/repo"
	"github.com/onelastcommit/noctra/internal/state"
)

type RepoResolver interface {
	AllRepoPaths() []string
	ResolveDirect(ctx context.Context, ref, branch string) (repo.Resolved, error)
}

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

	directiveBranch func(context.Context, string) string
}

func (s *Scheduler) SetDirectiveBranchResolver(fn func(context.Context, string) string) {
	s.directiveBranch = fn
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

type Job struct {
	Task       Task
	RepoPath   string
	RepoSlug   string
	MainBranch string
}

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

func (s *Scheduler) MarkSwept() {
	s.lastSweep = s.now()
}

type repoTarget struct {
	path   string
	branch string
}

func (s *Scheduler) repoTargets(ctx context.Context) []repoTarget {
	if len(s.sweepRepos) == 0 {
		paths := s.resolver.AllRepoPaths()
		targets := make([]repoTarget, 0, len(paths))
		for _, p := range paths {
			branch := ""
			if s.directiveBranch != nil {
				if remote := repo.OriginRemoteOf(ctx, p); remote != "" {
					branch = s.directiveBranch(ctx, remote)
				}
			}
			targets = append(targets, repoTarget{path: p, branch: branch})
		}
		return targets
	}
	var targets []repoTarget
	seen := make(map[string]bool)
	for _, entry := range s.sweepRepos {
		ref, branch := parseSweepRepoRef(entry)
		if branch == "" && s.directiveBranch != nil {
			branch = s.directiveBranch(ctx, ref)
		}
		res, err := s.resolver.ResolveDirect(ctx, ref, branch)
		if err != nil {
			slog.Warn("sweep: skipping repo it could not resolve", "repo", ref, "err", err)
			continue
		}
		if seen[res.Path] {
			continue
		}
		seen[res.Path] = true
		targets = append(targets, repoTarget{path: res.Path, branch: res.MainBranch})
	}
	return targets
}

func parseSweepRepoRef(entry string) (ref, branch string) {
	entry = strings.TrimSpace(entry)
	start := 0
	if i := strings.Index(entry, "://"); i >= 0 {
		if slash := strings.Index(entry[i+3:], "/"); slash >= 0 {
			start = i + 3 + slash
		} else {
			start = len(entry)
		}
	} else if colon := strings.Index(entry, ":"); colon >= 0 && strings.Contains(entry[:colon], "@") {
		start = colon
	}
	if at := strings.LastIndex(entry[start:], "@"); at >= 0 {
		idx := start + at
		return entry[:idx], entry[idx+1:]
	}
	return entry, ""
}

type PlanOptions struct {
	Tasks          []string
	Repos          []string
	IgnoreCooldown bool
	Limit          int
}

func (s *Scheduler) Plan(ctx context.Context) []Job {
	return s.PlanWith(ctx, PlanOptions{})
}

func (s *Scheduler) PlanWith(ctx context.Context, opts PlanOptions) []Job {
	targets := s.repoTargets(ctx)
	if len(targets) == 0 {
		slog.Debug("sweep: no repos to sweep")
		return nil
	}

	var groups [][]Job
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		slug := repo.SlugFromPath(t.path)
		if slug == "" {
			continue
		}
		if !matchesFilter(slug, opts.Repos) {
			continue
		}
		var eligible []Task
		for _, task := range s.tasks {
			if !matchesFilter(task.Name, opts.Tasks) {
				continue
			}
			key := state.SweepKey(slug, task.Name)
			ss := s.store.GetSweep(key)
			if !opts.IgnoreCooldown && !ss.LastRunAt.IsZero() && s.now().Sub(ss.LastRunAt) < task.Cooldown {
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
		mainBranch := t.branch
		if mainBranch == "" {
			mainBranch = repo.DefaultBranchOf(ctx, t.path)
		}
		repoJobs := make([]Job, len(eligible))
		for i, task := range eligible {
			repoJobs[i] = Job{Task: task, RepoPath: t.path, RepoSlug: slug, MainBranch: mainBranch}
		}
		groups = append(groups, repoJobs)
	}

	if n := len(groups); n > 0 {
		off := s.repoRotation % n
		groups = append(groups[off:], groups[:off]...)
		s.repoRotation++
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = s.maxTasks
	}
	jobs := roundRobin(groups, limit)
	slog.Info("sweep plan",
		"repos", len(targets),
		"tasks", len(s.tasks),
		"eligible", len(jobs),
		"max", limit,
		"forced", opts.IgnoreCooldown)
	return jobs
}

func matchesFilter(name string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		f = strings.TrimSpace(f)
		if f != "" && strings.Contains(strings.ToLower(name), strings.ToLower(f)) {
			return true
		}
	}
	return false
}

func (s *Scheduler) TaskNames() []string {
	names := make([]string, 0, len(s.tasks))
	for _, t := range s.tasks {
		names = append(names, t.Name)
	}
	return names
}

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

func (s *Scheduler) RecordRun(repoSlug, taskName string) error {
	key := state.SweepKey(repoSlug, taskName)
	return s.store.UpdateSweep(key, func(ss *state.SweepState) {
		ss.LastRunAt = s.now()
	})
}

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
