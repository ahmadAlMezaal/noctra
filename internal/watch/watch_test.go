package watch

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/github"
	"github.com/ahmadAlMezaal/nightshift/internal/state"
)

// newTestWatcher returns a Watcher with no gh client (we don't exercise gh
// here — diff is the unit we care about) and a fresh state store.
func newTestWatcher(t *testing.T, trusted []string) *Watcher {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(nil, store, nil, trusted)
}

func TestDiff_NewCommentByHumanIsActionable(t *testing.T) {
	w := newTestWatcher(t, nil)
	pr := github.PR{URL: "https://github.com/me/repo/pull/1", Number: 1}
	details := &github.Details{
		State: "OPEN",
		Comments: []github.Comment{{
			ID:        "C1",
			Author:    github.Actor{Login: "alice", Type: "User"},
			Body:      "Please fix the typo",
			CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 1 {
		t.Fatalf("expected 1 actionable event, got %d", len(ch.Events))
	}
	if ch.Events[0].Type != EventComment || ch.Events[0].Author.Login != "alice" {
		t.Errorf("event: %+v", ch.Events[0])
	}
	if !ch.NewestComment.Equal(details.Comments[0].CreatedAt) {
		t.Errorf("NewestComment cursor: got %v", ch.NewestComment)
	}
}

func TestDiff_InlineReviewCommentByHumanIsActionable(t *testing.T) {
	w := newTestWatcher(t, nil)
	pr := github.PR{URL: "https://github.com/me/repo/pull/1", Number: 1}
	details := &github.Details{
		State: "OPEN",
		ReviewComments: []github.ReviewComment{{
			Author:    github.Actor{Login: "alice", Type: "User"},
			Body:      "keep the mutex held here",
			CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
			Path:      "internal/state/state.go",
			Line:      122,
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 1 {
		t.Fatalf("expected 1 actionable event, got %d", len(ch.Events))
	}
	ev := ch.Events[0]
	if ev.Type != EventComment || ev.Path != "internal/state/state.go" || ev.Line != 122 {
		t.Errorf("event: %+v", ev)
	}
	if !ch.NewestComment.Equal(details.ReviewComments[0].CreatedAt) {
		t.Errorf("NewestComment cursor: got %v", ch.NewestComment)
	}
}

func TestDiff_UntrustedBotInlineCommentIsSkipped(t *testing.T) {
	w := newTestWatcher(t, nil) // humans only
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	details := &github.Details{
		State: "OPEN",
		ReviewComments: []github.ReviewComment{{
			Author:    github.Actor{Login: "gemini-code-assist", Type: "Bot"},
			Body:      "There is a critical race condition",
			CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
			Path:      "internal/state/state.go",
			Line:      122,
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 0 {
		t.Errorf("untrusted bot inline comment should be skipped, got %d events", len(ch.Events))
	}
	if len(ch.Skipped) != 1 {
		t.Errorf("expected 1 skipped event, got %d", len(ch.Skipped))
	}
	// Cursor must still advance past the skipped comment.
	if !ch.NewestComment.Equal(details.ReviewComments[0].CreatedAt) {
		t.Errorf("NewestComment cursor should advance past skipped comment: got %v", ch.NewestComment)
	}
}

func TestDiff_OldInlineReviewCommentIsIgnored(t *testing.T) {
	w := newTestWatcher(t, nil)
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	at := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	details := &github.Details{
		State: "OPEN",
		ReviewComments: []github.ReviewComment{{
			Author:    github.Actor{Login: "alice", Type: "User"},
			Body:      "old inline comment",
			CreatedAt: at,
		}},
	}

	ch := w.diff(pr, details, state.PRState{LastCommentAt: at})
	if len(ch.Events) != 0 {
		t.Errorf("inline comment at-or-before cursor should be ignored, got %d events", len(ch.Events))
	}
}

func TestDiff_OldCommentIsIgnored(t *testing.T) {
	w := newTestWatcher(t, nil)
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	commentAt := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	details := &github.Details{
		State: "OPEN",
		Comments: []github.Comment{{
			Author:    github.Actor{Login: "alice", Type: "User"},
			Body:      "old comment",
			CreatedAt: commentAt,
		}},
	}

	ch := w.diff(pr, details, state.PRState{LastCommentAt: commentAt})
	if len(ch.Events) != 0 {
		t.Errorf("comments at-or-before cursor should be ignored, got %d events", len(ch.Events))
	}
}

func TestDiff_UntrustedBotIsSkipped(t *testing.T) {
	w := newTestWatcher(t, nil) // empty trust list = humans only
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	details := &github.Details{
		State: "OPEN",
		Comments: []github.Comment{{
			Author:    github.Actor{Login: "gemini-code-assist", Type: "Bot"},
			Body:      "Consider this suggestion",
			CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 0 {
		t.Errorf("untrusted bot should be skipped, got %d actionable events", len(ch.Events))
	}
	if len(ch.Skipped) != 1 {
		t.Errorf("expected 1 skipped event, got %d", len(ch.Skipped))
	}
}

func TestDiff_TrustedBotIsActioned(t *testing.T) {
	w := newTestWatcher(t, []string{"gemini-code-assist"})
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	details := &github.Details{
		State: "OPEN",
		Comments: []github.Comment{{
			Author:    github.Actor{Login: "gemini-code-assist", Type: "Bot"},
			Body:      "Consider this suggestion",
			CreatedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 1 {
		t.Errorf("trusted bot should be actioned, got %d events", len(ch.Events))
	}
}

func TestDiff_ApprovedReviewIsIgnored(t *testing.T) {
	w := newTestWatcher(t, nil)
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	details := &github.Details{
		State: "OPEN",
		Reviews: []github.Review{{
			Author:      github.Actor{Login: "alice", Type: "User"},
			State:       "APPROVED",
			Body:        "LGTM",
			SubmittedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 0 {
		t.Errorf("APPROVED reviews should not produce actionable events, got %d", len(ch.Events))
	}
}

func TestDiff_ChangesRequestedReviewIsActionable(t *testing.T) {
	w := newTestWatcher(t, nil)
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	details := &github.Details{
		State: "OPEN",
		Reviews: []github.Review{{
			Author:      github.Actor{Login: "alice", Type: "User"},
			State:       "CHANGES_REQUESTED",
			Body:        "needs work in X",
			SubmittedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 1 {
		t.Errorf("CHANGES_REQUESTED should be actioned, got %d events", len(ch.Events))
	}
	if ch.Events[0].ReviewState != "CHANGES_REQUESTED" {
		t.Errorf("event ReviewState: got %q", ch.Events[0].ReviewState)
	}
}

func TestDiff_EmptyCommentedReviewIsIgnored(t *testing.T) {
	w := newTestWatcher(t, nil)
	pr := github.PR{URL: "https://github.com/me/repo/pull/1"}
	details := &github.Details{
		State: "OPEN",
		Reviews: []github.Review{{
			Author:      github.Actor{Login: "alice", Type: "User"},
			State:       "COMMENTED",
			Body:        "",
			SubmittedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		}},
	}

	ch := w.diff(pr, details, state.PRState{})
	if len(ch.Events) != 0 {
		t.Errorf("empty COMMENTED review (just a wrapper) should be ignored, got %d events", len(ch.Events))
	}
}
