package sweep

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

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
	s := NewScheduler(store, resolver, nil, 1*time.Hour, 5)

	// Just created — should not be due yet (suppress initial sweep).
	if due := s.DueIn(); due == 0 {
		t.Error("DueIn should not be 0 immediately after creation")
	}

	// Simulate time passing.
	past := time.Now().Add(-2 * time.Hour)
	s.lastSweep = past
	if due := s.DueIn(); due != 0 {
		t.Errorf("DueIn should be 0 after interval elapsed, got %v", due)
	}
}

func TestScheduler_MarkSwept(t *testing.T) {
	store, _ := state.Open(filepath.Join(t.TempDir(), "state.json"))
	resolver := &repo.Resolver{ReposBase: t.TempDir()}
	s := NewScheduler(store, resolver, nil, 1*time.Hour, 5)

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

	s := NewScheduler(store, resolver, tasks, time.Hour, 2) // max 2

	jobs := s.Plan(context.Background())
	if len(jobs) > 2 {
		t.Errorf("Plan returned %d jobs, max is 2", len(jobs))
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

	s := NewScheduler(store, resolver(reposBase), tasks, time.Hour, 10)

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

	s := NewScheduler(store, resolver, []Task{testTask("t1", time.Hour)}, time.Hour, 5)
	jobs := s.Plan(context.Background())
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs with no repos, got %d", len(jobs))
	}
}

func TestScheduler_RecordRun(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	store, _ := state.Open(statePath)

	resolver := &repo.Resolver{ReposBase: t.TempDir()}
	s := NewScheduler(store, resolver, nil, time.Hour, 5)

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

	s := NewScheduler(store, resolver, []Task{testTask("t1", 24*time.Hour)}, time.Hour, 5)
	summary := s.Summary()
	if summary == "" {
		t.Error("Summary should not be empty")
	}
}

func resolver(reposBase string) *repo.Resolver {
	return &repo.Resolver{ReposBase: reposBase}
}
