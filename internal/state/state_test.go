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

func TestOpen_CreatesSQLiteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	s, _ := Open(path)
	defer closeStore(t, s)
	if err := s.Update("a", func(r *PRState) { r.TicketID = "ENG-1" }); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("state db was not created: %v", err)
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

func TestOpenMigrating_MigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	jsonPath := filepath.Join(dir, "state.json")

	commentAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	reviewAt := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	iteratedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sweepAt := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	oauthExp := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	legacy := fileFormat{
		PRs: map[string]*PRState{
			"https://github.com/me/repo/pull/42": {
				TicketID:       "ENG-42",
				AgentBackend:   "codex",
				LastCommentAt:  commentAt,
				LastReviewAt:   reviewAt,
				LastCISHA:      "abc123",
				Iterations:     2,
				LastIteratedAt: iteratedAt,
			},
		},
		Sweeps: map[string]*SweepState{
			"repo/lint-cleanup": {LastRunAt: sweepAt},
		},
		OAuth: &OAuthState{
			AccessToken:  "access",
			ExpiresAt:    oauthExp,
			RefreshToken: "refresh",
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := OpenMigrating(dbPath, jsonPath)
	if err != nil {
		t.Fatalf("OpenMigrating: %v", err)
	}
	defer closeStore(t, s)

	pr := s.Get("https://github.com/me/repo/pull/42")
	if pr.TicketID != "ENG-42" || pr.AgentBackend != "codex" || pr.LastCISHA != "abc123" || pr.Iterations != 2 {
		t.Fatalf("PR state not migrated: %+v", pr)
	}
	if !pr.LastCommentAt.Equal(commentAt) || !pr.LastReviewAt.Equal(reviewAt) || !pr.LastIteratedAt.Equal(iteratedAt) {
		t.Fatalf("PR timestamps not migrated: %+v", pr)
	}
	if got := s.GetSweep("repo/lint-cleanup"); !got.LastRunAt.Equal(sweepAt) {
		t.Fatalf("sweep state not migrated: %+v", got)
	}
	access, exp, refresh := s.LoadOAuth()
	if access != "access" || refresh != "refresh" || !exp.Equal(oauthExp) {
		t.Fatalf("OAuth not migrated: access=%q exp=%v refresh=%q", access, exp, refresh)
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("legacy JSON should remain in place: %v", err)
	}
}

func TestOpenMigrating_DoesNotClobberExistingDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	jsonPath := filepath.Join(dir, "state.json")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.TicketID = "ENG-1"
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	legacy := fileFormat{PRs: map[string]*PRState{
		"https://github.com/me/repo/pull/1": {TicketID: "ENG-OLD"},
	}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenMigrating(dbPath, jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)
	if got := s2.Get("https://github.com/me/repo/pull/1").TicketID; got != "ENG-1" {
		t.Fatalf("existing DB was clobbered: got %q", got)
	}
}

func TestOpenMigrating_RemovesNewDBAfterFailedMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	jsonPath := filepath.Join(dir, "state.json")

	if err := os.WriteFile(jsonPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMigrating(dbPath, jsonPath); err == nil {
		t.Fatal("expected invalid legacy JSON to fail migration")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("failed migration left db at %s: stat err=%v", dbPath, err)
	}

	legacy := fileFormat{PRs: map[string]*PRState{
		"https://github.com/me/repo/pull/42": {TicketID: "ENG-42"},
	}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := OpenMigrating(dbPath, jsonPath)
	if err != nil {
		t.Fatalf("retry OpenMigrating: %v", err)
	}
	defer closeStore(t, s)
	if got := s.Get("https://github.com/me/repo/pull/42").TicketID; got != "ENG-42" {
		t.Fatalf("retry migration did not import PR state: got %q", got)
	}
}

func TestUpdate_ReturnsReadError(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.TicketID = "ENG-1"
		r.Iterations = 7
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	err = s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.Iterations = 0
	})
	if err == nil {
		t.Fatal("expected Update to return a read error after close")
	}
	if !strings.Contains(err.Error(), "read pr state") {
		t.Fatalf("Update error = %v, want read pr state context", err)
	}
}

// ── Plan state tests (ENG-221) ──────────────────────────────────────────────

func TestGetPlan_UnknownReturnsZero(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	got := s.GetPlan("ENG-999")
	if got != (PlanState{}) {
		t.Errorf("expected zero PlanState, got %+v", got)
	}
}

func TestSavePlan_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	ps := PlanState{
		IssueID:   "issue-uuid-123",
		Plan:      "## Summary\nDo the thing.",
		PlannedAt: now,
	}
	if err := s.SavePlan("ENG-42", ps); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and verify.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)
	got := s2.GetPlan("ENG-42")
	if got.IssueID != "issue-uuid-123" {
		t.Errorf("IssueID: got %q, want %q", got.IssueID, "issue-uuid-123")
	}
	if got.Plan != "## Summary\nDo the thing." {
		t.Errorf("Plan: got %q", got.Plan)
	}
	if !got.PlannedAt.Equal(now) {
		t.Errorf("PlannedAt: got %v, want %v", got.PlannedAt, now)
	}
}

func TestDeletePlan_RemovesRecord(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	if err := s.SavePlan("ENG-42", PlanState{Plan: "plan"}); err != nil {
		t.Fatal(err)
	}
	if got := s.GetPlan("ENG-42"); got.Plan == "" {
		t.Fatal("expected plan to be saved")
	}

	if err := s.DeletePlan("ENG-42"); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	if got := s.GetPlan("ENG-42"); got.Plan != "" {
		t.Errorf("expected plan to be deleted, got %+v", got)
	}
}

func TestDeletePlan_Idempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	// Deleting a non-existent plan should not error.
	if err := s.DeletePlan("ENG-999"); err != nil {
		t.Fatalf("DeletePlan on non-existent: %v", err)
	}
}

func TestAllPlans_ReturnsAllPending(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	if err := s.SavePlan("ENG-1", PlanState{Plan: "plan 1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan("ENG-2", PlanState{Plan: "plan 2"}); err != nil {
		t.Fatal(err)
	}

	got := s.AllPlans()
	if len(got) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(got))
	}
	if got["ENG-1"].Plan != "plan 1" || got["ENG-2"].Plan != "plan 2" {
		t.Errorf("unexpected plans: %+v", got)
	}
}

func TestAllPlans_EmptyWhenNone(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	got := s.AllPlans()
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

func TestSavePlan_UpsertOverwrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	if err := s.SavePlan("ENG-42", PlanState{Plan: "old plan"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan("ENG-42", PlanState{Plan: "new plan"}); err != nil {
		t.Fatal(err)
	}

	got := s.GetPlan("ENG-42")
	if got.Plan != "new plan" {
		t.Errorf("expected upsert to overwrite, got %q", got.Plan)
	}
}

func TestLessons(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	// Unset should return empty string.
	got, err := s.GetLessons("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty lessons, got %q", got)
	}

	// Save and retrieve should work.
	const lessons = "- Lesson 1: follow code conventions\n- Lesson 2: write tests"
	if err := s.SaveLessons("owner/repo", lessons); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetLessons("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got != lessons {
		t.Errorf("expected lessons %q, got %q", lessons, got)
	}

	// Upsert should overwrite.
	const newLessons = "- Lesson 1 updated"
	if err := s.SaveLessons("owner/repo", newLessons); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetLessons("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got != newLessons {
		t.Errorf("expected lessons %q, got %q", newLessons, got)
	}
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
