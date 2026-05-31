package state

import (
	"path/filepath"
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
