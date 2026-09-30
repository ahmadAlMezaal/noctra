package sweep

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/onelastcommit/noctra/internal/repo"
	"github.com/onelastcommit/noctra/internal/state"
)

type fakeResolver struct {
	all    []string
	direct map[string]string
}

func (f fakeResolver) AllRepoPaths() []string { return f.all }

func (f fakeResolver) ResolveDirect(_ context.Context, ref, branch string) (repo.Resolved, error) {
	if p, ok := f.direct[ref]; ok {
		if branch == "" {
			branch = "main"
		}
		return repo.Resolved{Path: p, MainBranch: branch}, nil
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

	if due := s.DueIn(); due != 0 {
		t.Errorf("DueIn should be 0 immediately after creation, got %v", due)
	}

	s.MarkSwept()
	if due := s.DueIn(); due == 0 {
		t.Error("DueIn should not be 0 immediately after MarkSwept")
	}

	s.lastSweep = time.Now().Add(-2 * time.Hour)
	if due := s.DueIn(); due != 0 {
		t.Errorf("DueIn should be 0 after interval elapsed, got %v", due)
	}
}

func TestScheduler_MarkSwept(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: t.TempDir()}
	s := NewScheduler(store, resolver, nil, 1*time.Hour, 5, nil, nil)

	s.lastSweep = time.Now().Add(-2 * time.Hour)
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

	s := NewScheduler(store, resolver, tasks, time.Hour, 2, nil, nil)

	jobs := s.Plan(context.Background())
	if len(jobs) > 2 {
		t.Errorf("Plan returned %d jobs, max is 2", len(jobs))
	}
}

func TestRoundRobin(t *testing.T) {
	eq := func(got, want []int) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	groups := [][]int{{1, 2, 3}, {4, 5}, {6}}
	if got := roundRobin(groups, 4); !eq(got, []int{1, 4, 6, 2}) {
		t.Errorf("limited spread: got %v, want [1 4 6 2]", got)
	}
	if got := roundRobin([][]int{{1, 2}, {3}}, 10); !eq(got, []int{1, 3, 2}) {
		t.Errorf("drain: got %v, want [1 3 2]", got)
	}
	if got := roundRobin(groups, 0); got != nil {
		t.Errorf("zero limit: got %v, want nil", got)
	}
	if len(groups[0]) != 3 {
		t.Errorf("roundRobin mutated its input: %v", groups)
	}
}

func TestScheduler_PlanRotatesLeadRepoAcrossCycles(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "repo-a")
	initTestRepo(t, reposBase, "repo-b")
	initTestRepo(t, reposBase, "repo-c")

	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	tasks := []Task{testTask("t1", time.Hour), testTask("t2", time.Hour)}

	s := NewScheduler(store, resolver(reposBase), tasks, time.Hour, 1, nil, nil)

	var leads []string
	for i := 0; i < 3; i++ {
		jobs := s.Plan(context.Background())
		if len(jobs) != 1 {
			t.Fatalf("cycle %d: expected 1 job, got %d", i, len(jobs))
		}
		leads = append(leads, jobs[0].RepoSlug)
	}
	seen := map[string]bool{}
	for _, l := range leads {
		seen[l] = true
	}
	if len(seen) != 3 {
		t.Errorf("lead repo should rotate across 3 cycles, got %v", leads)
	}
}

func TestScheduler_PlanSpreadsAcrossRepos(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "repo-a")
	initTestRepo(t, reposBase, "repo-b")
	initTestRepo(t, reposBase, "repo-c")

	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))

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

	jobs := s.Plan(context.Background())
	if len(jobs) != 2 {
		t.Fatalf("expected 2 eligible tasks, got %d", len(jobs))
	}

	if err := s.RecordRun("my-repo", "task-a"); err != nil {
		t.Fatal(err)
	}

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
	resolver := &repo.Resolver{ReposBase: t.TempDir()}

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

	key := state.SweepKey("my-repo", "lint-cleanup")
	ss := store.GetSweep(key)
	if ss.LastRunAt.IsZero() {
		t.Error("LastRunAt should be set after RecordRun")
	}

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

func TestParseSweepRepoRef(t *testing.T) {
	cases := []struct {
		entry, ref, branch string
	}{
		{"acme/wanted", "acme/wanted", ""},
		{"acme/wanted@staging", "acme/wanted", "staging"},
		{" acme/wanted@staging ", "acme/wanted", "staging"},
		{"https://github.com/acme/wanted", "https://github.com/acme/wanted", ""},
		{"https://github.com/acme/wanted@staging", "https://github.com/acme/wanted", "staging"},
		{"git@github.com:acme/wanted.git", "git@github.com:acme/wanted.git", ""},
		{"git@github.com:acme/wanted.git@staging", "git@github.com:acme/wanted.git", "staging"},
		{"ssh://git@github.com/acme/wanted", "ssh://git@github.com/acme/wanted", ""},
		{"ssh://git@github.com/acme/wanted@staging", "ssh://git@github.com/acme/wanted", "staging"},
		{"acme/wanted@release/1.2", "acme/wanted", "release/1.2"},
	}
	for _, c := range cases {
		ref, branch := parseSweepRepoRef(c.entry)
		if ref != c.ref || branch != c.branch {
			t.Errorf("parseSweepRepoRef(%q) = (%q, %q), want (%q, %q)", c.entry, ref, branch, c.ref, c.branch)
		}
	}
}

func TestPlan_BranchPrecedence(t *testing.T) {
	wanted := initTestRepo(t, t.TempDir(), "wanted-repo")
	mkScheduler := func(sweepRepos []string, directive func(context.Context, string) string) *Scheduler {
		store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
		res := fakeResolver{direct: map[string]string{"acme/wanted": wanted}}
		s := NewScheduler(store, res, []Task{testTask("t1", time.Hour)}, time.Hour, 5, nil, sweepRepos)
		if directive != nil {
			s.SetDirectiveBranchResolver(directive)
		}
		return s
	}
	planBranch := func(s *Scheduler) string {
		jobs := s.Plan(context.Background())
		if len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %d", len(jobs))
		}
		return jobs[0].MainBranch
	}

	if got := planBranch(mkScheduler([]string{"acme/wanted@override"}, func(context.Context, string) string { return "directive" })); got != "override" {
		t.Errorf("@branch override: got %q, want %q", got, "override")
	}
	if got := planBranch(mkScheduler([]string{"acme/wanted"}, func(context.Context, string) string { return "staging" })); got != "staging" {
		t.Errorf("directive fallback: got %q, want %q", got, "staging")
	}
	if got := planBranch(mkScheduler([]string{"acme/wanted"}, nil)); got != "main" {
		t.Errorf("default fallback: got %q, want %q", got, "main")
	}
}

func TestPlan_DiscoveryDirectiveBranch(t *testing.T) {
	reposBase := t.TempDir()
	dir := initTestRepo(t, reposBase, "wanted-repo")
	addOrigin(t, dir, "https://github.com/acme/wanted")

	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: reposBase}
	s := NewScheduler(store, resolver, []Task{testTask("t1", time.Hour)}, time.Hour, 5, nil, nil)
	s.SetDirectiveBranchResolver(func(_ context.Context, ref string) string {
		if ref == "https://github.com/acme/wanted" {
			return "staging"
		}
		return ""
	})

	jobs := s.Plan(context.Background())
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].MainBranch != "staging" {
		t.Errorf("discovery directive branch: got %q, want %q", jobs[0].MainBranch, "staging")
	}
}

func addOrigin(t *testing.T, dir, url string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "add", "origin", url)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %s: %v", out, err)
	}
}

func TestPlanWith_IgnoreCooldown(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "my-repo")
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))

	tasks := []Task{testTask("task-a", 24*time.Hour), testTask("task-b", 24*time.Hour)}
	s := NewScheduler(store, resolver(reposBase), tasks, time.Hour, 10, nil, nil)

	if err := s.RecordRun("my-repo", "task-a"); err != nil {
		t.Fatal(err)
	}
	if jobs := s.Plan(context.Background()); len(jobs) != 1 {
		t.Fatalf("cooldown not enforced on the scheduled path: got %d jobs, want 1", len(jobs))
	}

	jobs := s.PlanWith(context.Background(), PlanOptions{IgnoreCooldown: true})
	if len(jobs) != 2 {
		t.Fatalf("IgnoreCooldown: got %d jobs, want 2", len(jobs))
	}
}

func TestPlanWith_TaskAndRepoFilters(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "alpha-repo")
	initTestRepo(t, reposBase, "beta-repo")
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))

	tasks := []Task{testTask("task-a", 24*time.Hour), testTask("task-b", 24*time.Hour)}
	s := NewScheduler(store, resolver(reposBase), tasks, time.Hour, 10, nil, nil)

	jobs := s.PlanWith(context.Background(), PlanOptions{Tasks: []string{"task-a"}})
	if len(jobs) != 2 {
		t.Fatalf("task filter: got %d jobs, want 2", len(jobs))
	}
	for _, j := range jobs {
		if j.Task.Name != "task-a" {
			t.Errorf("task filter leaked %q", j.Task.Name)
		}
	}

	jobs = s.PlanWith(context.Background(), PlanOptions{Tasks: []string{"task-a"}, Repos: []string{"alpha"}})
	if len(jobs) != 1 {
		t.Fatalf("task+repo filter: got %d jobs, want 1", len(jobs))
	}
	if jobs[0].RepoSlug != "alpha-repo" || jobs[0].Task.Name != "task-a" {
		t.Errorf("got %s/%s, want alpha-repo/task-a", jobs[0].RepoSlug, jobs[0].Task.Name)
	}
}

func TestPlanWith_UnmatchedFilterPlansNothing(t *testing.T) {
	reposBase := t.TempDir()
	initTestRepo(t, reposBase, "my-repo")
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))

	s := NewScheduler(store, resolver(reposBase), []Task{testTask("task-a", time.Hour)}, time.Hour, 10, nil, nil)

	if jobs := s.PlanWith(context.Background(), PlanOptions{Tasks: []string{"no-such-task"}}); len(jobs) != 0 {
		t.Fatalf("unmatched task filter: got %d jobs, want 0", len(jobs))
	}
	if jobs := s.PlanWith(context.Background(), PlanOptions{Repos: []string{"no-such-repo"}}); len(jobs) != 0 {
		t.Fatalf("unmatched repo filter: got %d jobs, want 0", len(jobs))
	}
}

func TestMatchesFilter(t *testing.T) {
	cases := []struct {
		name    string
		filters []string
		want    bool
	}{
		{"lint-cleanup", nil, true},
		{"lint-cleanup", []string{}, true},
		{"lint-cleanup", []string{"lint-cleanup"}, true},
		{"ahmadalmezaal-trade-mate", []string{"trade-mate"}, true},
		{"ahmadalmezaal-trade-mate", []string{"TRADE-MATE"}, true},
		{"lint-cleanup", []string{"dead-code"}, false},
		{"lint-cleanup", []string{"  "}, false},
		{"lint-cleanup", []string{"dead-code", "lint"}, true},
	}
	for _, c := range cases {
		if got := matchesFilter(c.name, c.filters); got != c.want {
			t.Errorf("matchesFilter(%q, %v) = %v, want %v", c.name, c.filters, got, c.want)
		}
	}
}
