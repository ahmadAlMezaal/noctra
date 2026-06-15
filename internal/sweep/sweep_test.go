package sweep

import (
	"testing"
)

func TestCatalog_ContainsRegisteredTasks(t *testing.T) {
	tasks := Catalog()
	if len(tasks) < 2 {
		t.Fatalf("expected at least 2 registered tasks, got %d", len(tasks))
	}

	// Verify the two core tasks exist.
	names := map[string]bool{}
	for _, task := range tasks {
		names[task.Name] = true
	}
	for _, want := range []string{"lint-cleanup", "dead-code"} {
		if !names[want] {
			t.Errorf("missing expected task %q", want)
		}
	}
}

func TestCatalog_TaskFields(t *testing.T) {
	for _, task := range Catalog() {
		if task.Name == "" {
			t.Error("task has empty Name")
		}
		if task.Description == "" {
			t.Errorf("task %q has empty Description", task.Name)
		}
		if task.Cooldown <= 0 {
			t.Errorf("task %q has non-positive Cooldown: %v", task.Name, task.Cooldown)
		}
		if task.BranchSuffix == "" {
			t.Errorf("task %q has empty BranchSuffix", task.Name)
		}
		if task.CommitPrefix == "" {
			t.Errorf("task %q has empty CommitPrefix", task.Name)
		}
		if task.Prompt == nil {
			t.Errorf("task %q has nil Prompt function", task.Name)
		}
		// Verify prompt can be called without panic.
		if task.Prompt != nil {
			p := task.Prompt("/tmp/test-repo")
			if p == "" {
				t.Errorf("task %q produced empty prompt", task.Name)
			}
		}
	}
}

func TestCatalog_UniqueBranchSuffixes(t *testing.T) {
	seen := map[string]string{} // suffix → task name
	for _, task := range Catalog() {
		if prev, ok := seen[task.BranchSuffix]; ok {
			t.Errorf("duplicate BranchSuffix %q: used by both %q and %q",
				task.BranchSuffix, prev, task.Name)
		}
		seen[task.BranchSuffix] = task.Name
	}
}

func TestFilterTasks_NilReturnsAll(t *testing.T) {
	all := Catalog()
	filtered := FilterTasks(nil)
	if len(filtered) != len(all) {
		t.Errorf("FilterTasks(nil): got %d tasks, want %d", len(filtered), len(all))
	}
}

func TestFilterTasks_EmptyReturnsAll(t *testing.T) {
	all := Catalog()
	filtered := FilterTasks([]string{})
	if len(filtered) != len(all) {
		t.Errorf("FilterTasks([]): got %d tasks, want %d", len(filtered), len(all))
	}
}

func TestFilterTasks_SelectsSubset(t *testing.T) {
	filtered := FilterTasks([]string{"lint-cleanup"})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 task, got %d", len(filtered))
	}
	if filtered[0].Name != "lint-cleanup" {
		t.Errorf("expected lint-cleanup, got %q", filtered[0].Name)
	}
}

func TestFilterTasks_UnknownNameIgnored(t *testing.T) {
	filtered := FilterTasks([]string{"nonexistent"})
	if len(filtered) != 0 {
		t.Errorf("expected 0 tasks for unknown name, got %d", len(filtered))
	}
}

func TestSweepBranchName(t *testing.T) {
	tests := []struct {
		repoSlug string
		suffix   string
		want     string
	}{
		{"repo-a", "lint-cleanup", "noctra/sweep-repo-a-lint-cleanup"},
		{"Repo-B", "dead-code", "noctra/sweep-repo-b-dead-code"},
	}
	for _, tt := range tests {
		got := SweepBranchName(tt.repoSlug, tt.suffix)
		if got != tt.want {
			t.Errorf("SweepBranchName(%q, %q) = %q, want %q", tt.repoSlug, tt.suffix, got, tt.want)
		}
	}
}

func TestSweepIdentifier(t *testing.T) {
	got := SweepIdentifier("my-repo", "lint-cleanup")
	want := "SWEEP-MY-REPO-LINT-CLEANUP"
	if got != want {
		t.Errorf("SweepIdentifier = %q, want %q", got, want)
	}
}
