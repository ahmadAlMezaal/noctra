package sweep

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

type fakeResolver struct {
	all    []string
	direct map[string]string
}

func (f fakeResolver) AllRepoPaths() []string { return f.all }

func (f fakeResolver) ResolveDirect(_ context.Context, ref, _ string) (repo.Resolved, error) {
	if p, ok := f.direct[ref]; ok {
		return repo.Resolved{Path: p, MainBranch: "main"}, nil
	}
	return repo.Resolved{}, fmt.Errorf("unknown repo %q", ref)
}

func TestPlan_SweepReposOverridesDiscovery(t *testing.T) {
	base := t.TempDir()
	wanted := initTestRepo(t, base, "wanted-repo")
	other := initTestRepo(t, base, "other-repo")
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))

	res := fakeResolver{
		all:    []string{other},
		direct: map[string]string{"acme/wanted": wanted},
	}
	s := NewScheduler(store, res, []Task{testTask("t1", time.Hour)}, time.Hour, 5, nil, []string{"acme/wanted"})

	jobs := s.Plan(context.Background())
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].RepoPath != wanted {
		t.Errorf("swept %q, want the explicit repo %q (discovery should be ignored)", jobs[0].RepoPath, wanted)
	}
}

func TestPlan_SweepReposDeduplicates(t *testing.T) {
	base := t.TempDir()
	wanted := initTestRepo(t, base, "wanted-repo")
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))

	res := fakeResolver{direct: map[string]string{
		"acme/wanted":                    wanted,
		"git@github.com:acme/wanted.git": wanted,
	}}
	s := NewScheduler(store, res, []Task{testTask("t1", time.Hour)}, time.Hour, 5, nil,
		[]string{"acme/wanted", "git@github.com:acme/wanted.git"})

	if jobs := s.Plan(context.Background()); len(jobs) != 1 {
		t.Errorf("expected 1 job (equivalent refs dedup to one repo), got %d", len(jobs))
	}
}

func TestPlan_SweepReposSkipsUnresolvable(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	res := fakeResolver{direct: map[string]string{}}
	s := NewScheduler(store, res, []Task{testTask("t1", time.Hour)}, time.Hour, 5, nil, []string{"acme/missing"})

	if jobs := s.Plan(context.Background()); len(jobs) != 0 {
		t.Errorf("expected 0 jobs when nothing resolves, got %d", len(jobs))
	}
}

// testTask returns a minimal task for testing.
func testTask(name string, cooldown time.Duration) Task {
	return Task{
		Name:         name,
		Description:  "Test task: " + name,
		Cooldown:     cooldown,
		BranchSuffix: name,
		CommitPrefix: "test",
		Prompt:       func(repoPath string) string { return "test prompt for " + repoPath },
	}
}

// initTestRepo creates a minimal git repo and returns its path.
func initTestRepo(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %s: %v", out, err)
		}
	}
	return dir
}

func TestScheduler_DueIn(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: t.TempDir()}
	s := NewScheduler(store, resolver, nil, 1*time.Hour, 5, nil, nil)

	// Just created — should be immediately due (no startup suppression;
	// per-task cooldowns prevent spam).
	if due := s.DueIn(); due != 0 {
		t.Errorf("DueIn should be 0 immediately after creation, got %v", due)
	}

	// After marking swept, should not be due until interval elapses.
	s.MarkSwept()
	if due := s.DueIn(); due == 0 {
		t.Error("DueIn should not be 0 immediately after MarkSwept")
	}

	// Simulate time passing beyond the interval.
	s.lastSweep = time.Now().Add(-2 * time.Hour)
	if due := s.DueIn(); due != 0 {
		t.Errorf("DueIn should be 0 after interval elapsed, got %v", due)
	}
}

func TestScheduler_MarkSwept(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: t.TempDir()}
	s := NewScheduler(store, resolver, nil, 1*time.Hour, 5, nil, nil)

	s.lastSweep = time.Now().Add(-2 * time.Hour) // make it due
	s.MarkSwept()

	if due := s.DueIn(); due == 0 {
		t.Error("DueIn should not be 0 immediately after MarkSwept")
	}
}

func TestScheduler_PlanRespectsMaxTasks(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "repo-a")
	initTestRepo(t, reposBase, "repo-b")

	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: reposBase}

	tasks := []Task{
		testTask("t1", time.Hour),
		testTask("t2", time.Hour),
		testTask("t3", time.Hour),
	}

	s := NewScheduler(store, resolver, tasks, time.Hour, 2, nil, nil) // max 2

	jobs := s.Plan(context.Background())
	if len(jobs) > 2 {
		t.Errorf("Plan returned %d jobs, max is 2", len(jobs))
	}
}

func TestScheduler_PlanSpreadsAcrossRepos(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "repo-a")
	initTestRepo(t, reposBase, "repo-b")
	initTestRepo(t, reposBase, "repo-c")

	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))

	// 3 repos, each offering 3 tasks, budget 4. Round-robin should hand the
	// first 3 slots to one task each from a, b, c — not 3 tasks from repo-a.
	tasks := []Task{
		testTask("t1", time.Hour),
		testTask("t2", time.Hour),
		testTask("t3", time.Hour),
	}
	s := NewScheduler(store, resolver(reposBase), tasks, time.Hour, 4, nil, nil)

	jobs := s.Plan(context.Background())
	if len(jobs) != 4 {
		t.Fatalf("expected 4 jobs, got %d", len(jobs))
	}
	repos := map[string]int{}
	for _, j := range jobs {
		repos[j.RepoSlug]++
	}
	if len(repos) != 3 {
		t.Errorf("expected jobs across all 3 repos, got %v", repos)
	}
	for slug, n := range repos {
		if n > 2 {
			t.Errorf("repo %s got %d jobs; round-robin should cap the spread", slug, n)
		}
	}
}

func TestScheduler_PlanRespectsCooldown(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "my-repo")

	statePath := filepath.Join(t.TempDir(), "state.json")
	store, _ := state.Open(statePath)

	tasks := []Task{
		testTask("task-a", 24*time.Hour),
		testTask("task-b", 24*time.Hour),
	}

	s := NewScheduler(store, resolver(reposBase), tasks, time.Hour, 10, nil, nil)

	// Both should be eligible initially.
	jobs := s.Plan(context.Background())
	if len(jobs) != 2 {
		t.Fatalf("expected 2 eligible tasks, got %d", len(jobs))
	}

	// Record one run.
	if err := s.RecordRun("my-repo", "task-a"); err != nil {
		t.Fatal(err)
	}

	// Now only task-b should be eligible.
	jobs = s.Plan(context.Background())
	if len(jobs) != 1 {
		t.Fatalf("expected 1 eligible task after recording, got %d", len(jobs))
	}
	if jobs[0].Task.Name != "task-b" {
		t.Errorf("expected task-b, got %q", jobs[0].Task.Name)
	}
}

func TestScheduler_PlanNoRepos(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: t.TempDir()} // empty

	s := NewScheduler(store, resolver, []Task{testTask("t1", time.Hour)}, time.Hour, 5, nil, nil)
	jobs := s.Plan(context.Background())
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs with no repos, got %d", len(jobs))
	}
}

func TestScheduler_RecordRun(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	store, _ := state.Open(statePath)

	resolver := &repo.Resolver{ReposBase: t.TempDir()}
	s := NewScheduler(store, resolver, nil, time.Hour, 5, nil, nil)

	if err := s.RecordRun("my-repo", "lint-cleanup"); err != nil {
		t.Fatal(err)
	}

	// Verify state persisted.
	key := state.SweepKey("my-repo", "lint-cleanup")
	ss := store.GetSweep(key)
	if ss.LastRunAt.IsZero() {
		t.Error("LastRunAt should be set after RecordRun")
	}

	// Verify persists across reopen.
	store2, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ss2 := store2.GetSweep(key)
	if ss2.LastRunAt.IsZero() {
		t.Error("LastRunAt should persist across reopen")
	}
}

func TestScheduler_Summary(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: t.TempDir()}

	s := NewScheduler(store, resolver, []Task{testTask("t1", 24*time.Hour)}, time.Hour, 5, nil, nil)
	summary := s.Summary()
	if summary == "" {
		t.Error("Summary should not be empty")
	}
}

func resolver(reposBase string) *repo.Resolver {
	return &repo.Resolver{ReposBase: reposBase}
}

func TestDueIn_CronWaitsForNextMatch(t *testing.T) {
	sch, err := ParseCron("0 0 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	s := NewScheduler(nil, nil, nil, time.Hour, 5, sch, nil)
	base := time.Date(2026, 6, 20, 14, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	s.startedAt = base

	got := s.DueIn()
	want := 10 * time.Hour
	if got != want {
		t.Errorf("DueIn = %v, want %v", got, want)
	}
}

func TestDueIn_UnmatchableCronFallsBackToInterval(t *testing.T) {
	sch, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	s := NewScheduler(nil, nil, nil, time.Hour, 5, sch, nil)
	base := time.Date(2026, 6, 20, 14, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	s.lastSweep = base.Add(-30 * time.Minute)

	got := s.DueIn()
	want := 30 * time.Minute
	if got != want {
		t.Errorf("unmatchable cron DueIn = %v, want %v (interval fallback, not 0-spin)", got, want)
	}
}

func TestDueIn_CronCachesNextComputation(t *testing.T) {
	sch, _ := ParseCron("0 0 * * *")
	s := NewScheduler(nil, nil, nil, time.Hour, 5, sch, nil)
	base := time.Date(2026, 6, 20, 14, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	s.startedAt = base

	_ = s.DueIn()
	first := s.nextScheduled
	if first.IsZero() {
		t.Fatal("nextScheduled not cached after first DueIn")
	}
	_ = s.DueIn()
	if !s.nextScheduled.Equal(first) {
		t.Error("nextScheduled changed without the reference changing (not cached)")
	}
}
