package pipeline

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/onelastcommit/noctra/internal/agent"
	"github.com/onelastcommit/noctra/internal/state"
)

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRecordSweepPRStoresTheHeadLessonsNeed(t *testing.T) {
	dir := gitRepoWithUpstream(t)
	store := openTestStore(t)
	p := &Pipeline{store: store}
	backend, err := agent.New("claude")
	if err != nil {
		t.Fatal(err)
	}
	const prURL = "https://github.com/o/r/pull/7"

	p.recordSweepPR(context.Background(), prURL, "SWEEP-O-R-LINT-CLEANUP", backend, dir, slog.Default())

	got := store.Get(prURL)
	if got.LastPushedSHA == "" || got.LastPushedSHA != gitHead(context.Background(), dir) {
		t.Errorf("LastPushedSHA = %q, want the worktree HEAD", got.LastPushedSHA)
	}
	if got.TicketID != "SWEEP-O-R-LINT-CLEANUP" || got.AgentBackend != "claude" {
		t.Errorf("state = %+v, want the sweep identifier and backend recorded", got)
	}
}

func TestRepoLessonsReadsStoredLessons(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveLessons("o-r", "- Use semicolons"); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{store: store}

	if got := p.repoLessons("o-r"); got != "- Use semicolons" {
		t.Errorf("repoLessons = %q", got)
	}
	if got := p.repoLessons("unknown"); got != "" {
		t.Errorf("repoLessons(unknown) = %q, want empty", got)
	}
	if got := (&Pipeline{}).repoLessons("o-r"); got != "" {
		t.Errorf("repoLessons without a store = %q, want empty", got)
	}
}
