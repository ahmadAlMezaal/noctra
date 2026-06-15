package pipeline

import (
	"context"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/sweep"
	"github.com/ahmadAlMezaal/noctra/internal/watch"
)

func TestCountEvents_MixedTypes(t *testing.T) {
	events := []watch.Event{
		{Type: watch.EventComment},
		{Type: watch.EventReview},
		{Type: watch.EventComment},
		{Type: watch.EventComment},
		{Type: watch.EventReview},
	}
	comments, reviews := countEvents(events)
	if comments != 3 {
		t.Errorf("comments: got %d, want 3", comments)
	}
	if reviews != 2 {
		t.Errorf("reviews: got %d, want 2", reviews)
	}
}

func TestCountEvents_Empty(t *testing.T) {
	comments, reviews := countEvents(nil)
	if comments != 0 || reviews != 0 {
		t.Errorf("empty: got comments=%d reviews=%d", comments, reviews)
	}
}

func TestCountEvents_CommentsOnly(t *testing.T) {
	events := []watch.Event{
		{Type: watch.EventComment},
		{Type: watch.EventComment},
	}
	comments, reviews := countEvents(events)
	if comments != 2 {
		t.Errorf("comments: got %d, want 2", comments)
	}
	if reviews != 0 {
		t.Errorf("reviews: got %d, want 0", reviews)
	}
}

func TestCountEvents_ReviewsOnly(t *testing.T) {
	events := []watch.Event{
		{Type: watch.EventReview},
	}
	comments, reviews := countEvents(events)
	if comments != 0 {
		t.Errorf("comments: got %d, want 0", comments)
	}
	if reviews != 1 {
		t.Errorf("reviews: got %d, want 1", reviews)
	}
}

func TestGitHeadShort(t *testing.T) {
	dir := gitRepoWithUpstream(t) // from git_test.go
	ctx := context.Background()
	sha := gitHeadShort(ctx, dir)
	if sha == "" {
		t.Fatal("expected non-empty SHA")
	}
	if len(sha) < 7 {
		t.Errorf("SHA too short: %q", sha)
	}
}

func TestGitHeadShort_InvalidDir(t *testing.T) {
	ctx := context.Background()
	sha := gitHeadShort(ctx, t.TempDir())
	if sha != "" {
		t.Errorf("expected empty SHA for non-git dir, got %q", sha)
	}
}

func TestIdentifierFromBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"noctra/eng-42", "ENG-42"},
		{"noctra/eng-181", "ENG-181"},
		{"noctra/sweep-repo-a-lint-cleanup", "SWEEP-REPO-A-LINT-CLEANUP"},
		{"main", ""},
		{"feature/something", ""},
		{"noctra/", ""},
	}
	for _, tt := range tests {
		got := identifierFromBranch(tt.branch)
		if got != tt.want {
			t.Errorf("identifierFromBranch(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

func TestIdentifierFromBranch_SweepRoundTrip(t *testing.T) {
	repoSlug := "Owner/Repo-A"
	taskSuffix := "lint-cleanup"

	got := identifierFromBranch(sweep.SweepBranchName(repoSlug, taskSuffix))
	want := sweep.SweepIdentifier(repoSlug, taskSuffix)
	if got != want {
		t.Errorf("sweep branch identifier = %q, want %q", got, want)
	}
}
