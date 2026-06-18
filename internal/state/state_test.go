package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpen_MissingFileStartsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(s.All()) != 0 {
		t.Errorf("expected empty store, got %v", s.All())
	}
}

func TestUpdate_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	commentTs := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	const prURL = "https://github.com/me/repo/pull/42"
	if err := s.Update(prURL, func(r *PRState) {
		r.TicketID = "ENG-42"
		r.LastCommentAt = commentTs
		r.LastCISHA = "abc123"
		r.Iterations = 1
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Reopen and verify the values came back.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get(prURL)
	if got.TicketID != "ENG-42" {
		t.Errorf("TicketID: got %q", got.TicketID)
	}
	if !got.LastCommentAt.Equal(commentTs) {
		t.Errorf("LastCommentAt: got %v, want %v", got.LastCommentAt, commentTs)
	}
	if got.Iterations != 1 {
		t.Errorf("Iterations: got %d", got.Iterations)
	}
	if got.LastCISHA != "abc123" {
		t.Errorf("LastCISHA: got %q", got.LastCISHA)
	}
}

func TestUpdate_MultipleCallsAccumulate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	const prURL = "https://github.com/me/repo/pull/1"
	for i := 0; i < 3; i++ {
		if err := s.Update(prURL, func(r *PRState) {
			r.Iterations++
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	if got := s.Get(prURL).Iterations; got != 3 {
		t.Errorf("Iterations: got %d, want 3", got)
	}
}

func TestGet_UnknownPRReturnsZero(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get("https://nope")
	if got != (PRState{}) {
		t.Errorf("expected zero PRState, got %+v", got)
	}
}

func TestWriteAtomic_LeavesNoTmpFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, _ := Open(path)
	if err := s.Update("a", func(r *PRState) { r.TicketID = "ENG-1" }); err != nil {
		t.Fatal(err)
	}

	entries, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*"))
	if len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestSweepKey(t *testing.T) {
	got := SweepKey("my-repo", "lint-cleanup")
	want := "my-repo/lint-cleanup"
	if got != want {
		t.Errorf("SweepKey = %q, want %q", got, want)
	}
}

func TestGetSweep_UnknownReturnsZero(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := s.GetSweep("nonexistent/task")
	if got != (SweepState{}) {
		t.Errorf("expected zero SweepState, got %+v", got)
	}
}

func TestUpdateSweep_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	key := SweepKey("my-repo", "lint-cleanup")
	if err := s.UpdateSweep(key, func(ss *SweepState) {
		ss.LastRunAt = now
	}); err != nil {
		t.Fatalf("UpdateSweep: %v", err)
	}

	// Reopen and verify.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.GetSweep(key)
	if !got.LastRunAt.Equal(now) {
		t.Errorf("LastRunAt: got %v, want %v", got.LastRunAt, now)
	}
}

func TestUpdateSweep_CoexistsWithPRState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)

	// Write a PR state entry.
	if err := s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.TicketID = "ENG-1"
	}); err != nil {
		t.Fatal(err)
	}

	// Write a sweep state entry.
	if err := s.UpdateSweep("my-repo/lint", func(ss *SweepState) {
		ss.LastRunAt = time.Now()
	}); err != nil {
		t.Fatal(err)
	}

	// Reopen and verify both exist.
	s2, _ := Open(path)
	pr := s2.Get("https://github.com/me/repo/pull/1")
	if pr.TicketID != "ENG-1" {
		t.Errorf("PR state lost: TicketID = %q", pr.TicketID)
	}
	sw := s2.GetSweep("my-repo/lint")
	if sw.LastRunAt.IsZero() {
		t.Error("Sweep state lost: LastRunAt is zero")
	}
}

func TestOpen_MigratesLegacyJSONPRAndSweepState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	jsonPath := filepath.Join(dir, "state.json")

	commentTs := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	sweepTs := time.Date(2026, 6, 2, 11, 0, 0, 0, time.UTC)
	legacy := fileFormat{
		PRs: map[string]*PRState{
			"https://github.com/me/repo/pull/42": {
				TicketID:       "ENG-42",
				AgentBackend:   "codex",
				LastCommentAt:  commentTs,
				LastCISHA:      "abc123",
				Iterations:     2,
				LastIteratedAt: commentTs.Add(time.Hour),
			},
		},
		Sweeps: map[string]*SweepState{
			SweepKey("repo", "lint-cleanup"): {LastRunAt: sweepTs},
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath, jsonPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	got := s.Get("https://github.com/me/repo/pull/42")
	if got.TicketID != "ENG-42" || got.AgentBackend != "codex" || got.LastCISHA != "abc123" || got.Iterations != 2 {
		t.Fatalf("PR state not migrated: %+v", got)
	}
	if !got.LastCommentAt.Equal(commentTs) {
		t.Errorf("LastCommentAt: got %v, want %v", got.LastCommentAt, commentTs)
	}
	if sw := s.GetSweep(SweepKey("repo", "lint-cleanup")); !sw.LastRunAt.Equal(sweepTs) {
		t.Errorf("Sweep LastRunAt: got %v, want %v", sw.LastRunAt, sweepTs)
	}
}

func TestOpen_LegacyMigrationIsIdempotentAndDoesNotClobberDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	jsonPath := filepath.Join(dir, "state.json")
	const prURL = "https://github.com/me/repo/pull/42"

	writeLegacy := func(ticket string) {
		t.Helper()
		raw, err := json.Marshal(fileFormat{
			PRs: map[string]*PRState{prURL: {TicketID: ticket}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeLegacy("ENG-42")
	s, err := Open(dbPath, jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(prURL, func(r *PRState) { r.TicketID = "ENG-99" }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	writeLegacy("ENG-42")
	s2, err := Open(dbPath, jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got := s2.Get(prURL).TicketID; got != "ENG-99" {
		t.Errorf("migration clobbered existing DB row: got %q, want ENG-99", got)
	}
}

func TestUpdate_ReturnsReadError(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.TicketID = "ENG-1"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE pr_states`); err != nil {
		t.Fatal(err)
	}

	err = s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.TicketID = ""
		r.Iterations = 0
	})
	if err == nil {
		t.Fatal("expected Update to return a read error")
	}
	if !strings.Contains(err.Error(), "load pr state for update") {
		t.Fatalf("expected read error context, got %v", err)
	}
}
