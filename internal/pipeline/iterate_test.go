package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/onelastcommit/noctra/internal/github"
	"github.com/onelastcommit/noctra/internal/sweep"
	"github.com/onelastcommit/noctra/internal/watch"
)

func TestHasConversationComment(t *testing.T) {
	cases := []struct {
		name   string
		events []watch.Event
		want   bool
	}{
		{"conversation comment", []watch.Event{{Type: watch.EventComment}}, true},
		{"inline review comment has a path", []watch.Event{{Type: watch.EventComment, Path: "main.go"}}, false},
		{"review summary is not a comment", []watch.Event{{Type: watch.EventReview}}, false},
		{"mixed picks up the conversation comment", []watch.Event{{Type: watch.EventReview}, {Type: watch.EventComment, Path: "a.go"}, {Type: watch.EventComment}}, true},
		{"none", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasConversationComment(watch.PRChanges{Events: c.events}); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestConversationCommentAuthors(t *testing.T) {
	ch := watch.PRChanges{Events: []watch.Event{
		{Type: watch.EventComment, Author: github.Actor{Login: "alice"}},
		{Type: watch.EventComment, Path: "main.go", Author: github.Actor{Login: "bot"}},
		{Type: watch.EventReview, Author: github.Actor{Login: "gemini"}},
		{Type: watch.EventComment, Author: github.Actor{Login: "alice"}},
		{Type: watch.EventComment, Author: github.Actor{Login: "bob"}},
	}}
	got := conversationCommentAuthors(ch)
	want := []string{"@alice", "@bob"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("authors[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

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
	dir := gitRepoWithUpstream(t)
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

func TestShouldPostFallbackComment(t *testing.T) {
	cases := []struct {
		name        string
		ch          watch.PRChanges
		threadCount int
		want        bool
	}{
		{"CI-only, no events → no comment", watch.PRChanges{CIFailure: &watch.CIFailure{SHA: "abc"}}, 0, false},
		{"review feedback, no thread replies → comment", watch.PRChanges{Events: []watch.Event{{Type: watch.EventComment, Path: "a.go"}}}, 0, true},
		{"thread replies posted → no fallback", watch.PRChanges{Events: []watch.Event{{Type: watch.EventComment, Path: "a.go"}}}, 2, false},
		{"nothing at all → no comment", watch.PRChanges{}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPostFallbackComment(c.ch, c.threadCount); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"ENG-42", "ENG-42"},
		{"SWEEP-AHMADALMEZAAL-TRADE-MATE-LINT-CLEANUP", "Sweep: lint-cleanup on ahmadalmezaal-trade-mate"},
		{"SWEEP-MYREPO-DEAD-CODE", "Sweep: dead-code on myrepo"},
		{"SWEEP-UNKNOWN-TASK", "SWEEP-UNKNOWN-TASK"},
	}
	for _, tt := range tests {
		if got := displayName(tt.id); got != tt.want {
			t.Errorf("displayName(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestCapReason(t *testing.T) {
	if got := capReason("", ""); got != "" {
		t.Errorf("empty inputs = %q, want empty", got)
	}
	got := capReason("Fixed the import.\nMore detail.", "https://ci/run/1")
	want := "Fixed the import.\nFailing CI: https://ci/run/1"
	if got != want {
		t.Errorf("capReason = %q, want %q", got, want)
	}
	long := capReason(strings.Repeat("x", 400), "")
	if r := []rune(long); len(r) != 301 || string(r[300]) != "…" {
		t.Errorf("capReason did not truncate long reasoning: len=%d", len([]rune(long)))
	}
}
