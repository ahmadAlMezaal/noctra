package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen_MissingFileStartsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	defer s.Close() //nolint:errcheck
	if len(s.All()) != 0 {
		t.Errorf("expected empty store, got %v", s.All())
	}
}

func TestUpdate_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
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
	_ = s.Close()

	// Reopen and verify the values came back.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close() //nolint:errcheck
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
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

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
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	got := s.Get("https://nope")
	if got != (PRState{}) {
		t.Errorf("expected zero PRState, got %+v", got)
	}
}

func TestMigrateJSON_ImportsLegacyState(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "state.json")
	dbPath := filepath.Join(dir, "state.db")

	commentTs := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	reviewTs := time.Date(2026, 5, 31, 8, 30, 0, 0, time.UTC)
	iterTs := time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)

	legacy := struct {
		PRs map[string]*PRState `json:"prs"`
	}{
		PRs: map[string]*PRState{
			"https://github.com/me/repo/pull/1": {
				TicketID:       "ENG-1",
				AgentBackend:   "claude",
				LastCommentAt:  commentTs,
				LastReviewAt:   reviewTs,
				LastCISHA:      "sha1",
				Iterations:     2,
				LastIteratedAt: iterTs,
			},
			"https://github.com/me/repo/pull/2": {
				TicketID:   "ENG-2",
				Iterations: 1,
			},
		},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	if err := s.MigrateJSON(jsonPath); err != nil {
		t.Fatalf("MigrateJSON: %v", err)
	}

	// Verify all records made it.
	all := s.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 PRs, got %d", len(all))
	}

	pr1 := s.Get("https://github.com/me/repo/pull/1")
	if pr1.TicketID != "ENG-1" {
		t.Errorf("PR1 TicketID: got %q", pr1.TicketID)
	}
	if pr1.AgentBackend != "claude" {
		t.Errorf("PR1 AgentBackend: got %q", pr1.AgentBackend)
	}
	if !pr1.LastCommentAt.Equal(commentTs) {
		t.Errorf("PR1 LastCommentAt: got %v, want %v", pr1.LastCommentAt, commentTs)
	}
	if !pr1.LastReviewAt.Equal(reviewTs) {
		t.Errorf("PR1 LastReviewAt: got %v, want %v", pr1.LastReviewAt, reviewTs)
	}
	if pr1.LastCISHA != "sha1" {
		t.Errorf("PR1 LastCISHA: got %q", pr1.LastCISHA)
	}
	if pr1.Iterations != 2 {
		t.Errorf("PR1 Iterations: got %d", pr1.Iterations)
	}
	if !pr1.LastIteratedAt.Equal(iterTs) {
		t.Errorf("PR1 LastIteratedAt: got %v, want %v", pr1.LastIteratedAt, iterTs)
	}

	pr2 := s.Get("https://github.com/me/repo/pull/2")
	if pr2.TicketID != "ENG-2" {
		t.Errorf("PR2 TicketID: got %q", pr2.TicketID)
	}
	if pr2.Iterations != 1 {
		t.Errorf("PR2 Iterations: got %d", pr2.Iterations)
	}

	// JSON file should have been renamed.
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("expected JSON file to be renamed after migration")
	}
	if _, err := os.Stat(jsonPath + ".migrated"); err != nil {
		t.Errorf("expected .migrated file to exist: %v", err)
	}
}

func TestMigrateJSON_Idempotent(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "state.json")
	dbPath := filepath.Join(dir, "state.db")

	legacy := struct {
		PRs map[string]*PRState `json:"prs"`
	}{
		PRs: map[string]*PRState{
			"https://github.com/me/repo/pull/1": {
				TicketID:   "ENG-1",
				Iterations: 1,
			},
		},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	// First migration.
	if err := s.MigrateJSON(jsonPath); err != nil {
		t.Fatalf("MigrateJSON (first): %v", err)
	}

	// Update the record in the DB.
	if err := s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.Iterations = 5
	}); err != nil {
		t.Fatal(err)
	}

	// Re-create the JSON file and migrate again — should NOT overwrite.
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateJSON(jsonPath); err != nil {
		t.Fatalf("MigrateJSON (second): %v", err)
	}

	got := s.Get("https://github.com/me/repo/pull/1")
	if got.Iterations != 5 {
		t.Errorf("expected DB value to be preserved (5), got %d", got.Iterations)
	}
}

func TestMigrateJSON_MissingFileIsNoop(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	if err := s.MigrateJSON(filepath.Join(t.TempDir(), "nonexistent.json")); err != nil {
		t.Fatalf("MigrateJSON on missing file should be no-op: %v", err)
	}
}

func TestAll_ReturnsAllRecords(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck

	for i := 1; i <= 3; i++ {
		url := fmt.Sprintf("https://github.com/me/repo/pull/%d", i)
		tid := fmt.Sprintf("ENG-%d", i)
		if err := s.Update(url, func(r *PRState) {
			r.TicketID = tid
			r.Iterations = i
		}); err != nil {
			t.Fatal(err)
		}
	}

	all := s.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 PRs, got %d", len(all))
	}
	for i := 1; i <= 3; i++ {
		url := fmt.Sprintf("https://github.com/me/repo/pull/%d", i)
		if all[url].Iterations != i {
			t.Errorf("PR %d: expected iterations=%d, got %d", i, i, all[url].Iterations)
		}
	}
}
