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
		r.LastReasoning = "Fixed the nil check; skipped the rename suggestion as out of scope."
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

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
	if got.LastReasoning != "Fixed the nil check; skipped the rename suggestion as out of scope." {
		t.Errorf("LastReasoning: got %q", got.LastReasoning)
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

	if err := s.Update("https://github.com/me/repo/pull/1", func(r *PRState) {
		r.TicketID = "ENG-1"
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateSweep("my-repo/lint", func(ss *SweepState) {
		ss.LastRunAt = time.Now()
	}); err != nil {
		t.Fatal(err)
	}

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

	got, err := s.GetLessons("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty lessons, got %q", got)
	}

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

// ── Run history + usage event tests (ENG-280) ──────────────────────────────

func TestInsertAndListRunHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	now := time.Now().UTC().Truncate(time.Second)

	recs := []RunHistory{
		{
			Identifier: "ENG-1", TicketID: "ENG-1",
			PRURL: "https://github.com/o/r/pull/1", Repo: "my-repo",
			AgentBackend: "claude", RunType: "ticket",
			StartedAt: now.Add(-3 * time.Minute), FinishedAt: now.Add(-2 * time.Minute),
			Status: "pr_opened",
		},
		{
			Identifier: "ENG-2", TicketID: "ENG-2", Repo: "my-repo",
			AgentBackend: "codex", RunType: "ticket",
			StartedAt: now.Add(-1 * time.Minute), FinishedAt: now,
			Status: "failed",
		},
		{
			Identifier: "SWEEP-my-repo-lint", Repo: "my-repo",
			AgentBackend: "claude", RunType: "sweep",
			StartedAt: now, FinishedAt: now.Add(time.Minute),
			Status: "no_change",
		},
	}

	for _, r := range recs {
		if err := s.InsertRunHistory(r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := s.ListRunHistory(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len: got %d, want 3", len(got))
	}
	if got[0].Identifier != "SWEEP-my-repo-lint" {
		t.Errorf("newest first: got %s", got[0].Identifier)
	}
	if got[1].Identifier != "ENG-2" {
		t.Errorf("second: got %s", got[1].Identifier)
	}
	if got[2].Identifier != "ENG-1" {
		t.Errorf("third: got %s", got[2].Identifier)
	}

	if got[0].RunType != "sweep" {
		t.Errorf("run_type: got %q", got[0].RunType)
	}
	if got[0].Status != "no_change" {
		t.Errorf("status: got %q", got[0].Status)
	}
	if got[2].PRURL != "https://github.com/o/r/pull/1" {
		t.Errorf("pr_url: got %q", got[2].PRURL)
	}
	if got[2].Status != "pr_opened" {
		t.Errorf("status: got %q", got[2].Status)
	}

	got2, err := s.ListRunHistory(2)
	if err != nil {
		t.Fatalf("list(2): %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("len: got %d, want 2", len(got2))
	}
	if got2[0].Identifier != "SWEEP-my-repo-lint" {
		t.Errorf("bounded: got %s", got2[0].Identifier)
	}
}

func TestListRunHistory_Empty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	got, err := s.ListRunHistory(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

func TestRunHistory_TimestampRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	now := time.Now().UTC().Truncate(time.Second)
	rec := RunHistory{
		Identifier: "ENG-99", TicketID: "ENG-99", Repo: "test-repo",
		AgentBackend: "claude", RunType: "ticket",
		StartedAt: now, FinishedAt: now.Add(5 * time.Minute),
		Status: "pr_opened",
	}
	if err := s.InsertRunHistory(rec); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListRunHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len: got %d, want 1", len(got))
	}
	if got[0].StartedAt.Sub(now).Abs() > time.Second {
		t.Errorf("started_at drift: got %v, want ~%v", got[0].StartedAt, now)
	}
	if got[0].FinishedAt.Sub(now.Add(5*time.Minute)).Abs() > time.Second {
		t.Errorf("finished_at drift: got %v, want ~%v", got[0].FinishedAt, now.Add(5*time.Minute))
	}
}

func TestRecordUsage(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	now := time.Now().UTC().Truncate(time.Second)

	if err := s.RecordUsage(UsageEvent{
		OccurredAt: now, Source: "ticket", TicketID: "ENG-42",
		PRURL: "https://github.com/o/r/pull/7", AgentBackend: "claude",
		InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500, CostUSD: 0.03,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordUsage(UsageEvent{
		OccurredAt: now.Add(time.Minute), Source: "iterate", TicketID: "ENG-42",
		PRURL: "https://github.com/o/r/pull/7", AgentBackend: "claude",
		InputTokens: 2000, OutputTokens: 1000, TotalTokens: 3000, CostUSD: 0.06,
	}); err != nil {
		t.Fatal(err)
	}

	// Verify rows directly (no public read API for usage_events yet — P4).
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("count: got %d, want 2", count)
	}

	var source, ticketID string
	var inputTokens, totalTokens int64
	var costUSD float64
	err = s.db.QueryRow(`SELECT source, ticket_id, input_tokens, total_tokens, cost_usd
		FROM usage_events ORDER BY id LIMIT 1`).Scan(
		&source, &ticketID, &inputTokens, &totalTokens, &costUSD,
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if source != "ticket" {
		t.Errorf("source: got %q", source)
	}
	if ticketID != "ENG-42" {
		t.Errorf("ticket_id: got %q", ticketID)
	}
	if inputTokens != 1000 {
		t.Errorf("input_tokens: got %d", inputTokens)
	}
	if totalTokens != 1500 {
		t.Errorf("total_tokens: got %d", totalTokens)
	}
	if costUSD != 0.03 {
		t.Errorf("cost_usd: got %f", costUSD)
	}
}

func TestInsertRunHistory_SurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := s.InsertRunHistory(RunHistory{
		Identifier: "ENG-7", TicketID: "ENG-7", Repo: "repo",
		AgentBackend: "claude", RunType: "ticket",
		StartedAt: now, FinishedAt: now.Add(time.Minute),
		Status: "pr_opened", Iterations: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)

	got, err := s2.ListRunHistory(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Identifier != "ENG-7" || got[0].Status != "pr_opened" {
		t.Errorf("record did not survive reopen: %+v", got[0])
	}
}

// ── ListUsageEvents tests (ENG-277) ─────────────────────────────────────────

func TestListUsageEvents(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	now := time.Now().UTC().Truncate(time.Second)

	events := []UsageEvent{
		{OccurredAt: now.Add(-2 * time.Hour), Source: "ticket", TicketID: "ENG-1", CostUSD: 0.10, TotalTokens: 5000},
		{OccurredAt: now.Add(-1 * time.Hour), Source: "iterate", TicketID: "ENG-1", CostUSD: 0.05, TotalTokens: 2500},
		{OccurredAt: now, Source: "sweep", TicketID: "", CostUSD: 0.03, TotalTokens: 1500},
	}
	for _, ev := range events {
		if err := s.RecordUsage(ev); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListUsageEvents(time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(zero): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("all: got %d, want 3", len(got))
	}
	if got[0].Source != "ticket" || got[2].Source != "sweep" {
		t.Errorf("order: got %s..%s, want ticket..sweep", got[0].Source, got[2].Source)
	}

	got2, err := s.ListUsageEvents(now.Add(-90 * time.Minute))
	if err != nil {
		t.Fatalf("ListUsageEvents(since): %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("since: got %d, want 2", len(got2))
	}
	if got2[0].Source != "iterate" {
		t.Errorf("since first: got %q, want iterate", got2[0].Source)
	}
}

func TestListUsageEvents_Empty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	got, err := s.ListUsageEvents(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

// ── AllSweepStates tests (ENG-277) ──────────────────────────────────────────

func TestAllSweepStates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpdateSweep("repo-a/lint-cleanup", func(ss *SweepState) {
		ss.LastRunAt = now
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSweep("repo-b/dead-code", func(ss *SweepState) {
		ss.LastRunAt = now.Add(-24 * time.Hour)
	}); err != nil {
		t.Fatal(err)
	}

	got := s.AllSweepStates()
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	if !got["repo-a/lint-cleanup"].LastRunAt.Equal(now) {
		t.Errorf("repo-a/lint-cleanup: got %v, want %v", got["repo-a/lint-cleanup"].LastRunAt, now)
	}
	if !got["repo-b/dead-code"].LastRunAt.Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("repo-b/dead-code: got %v", got["repo-b/dead-code"].LastRunAt)
	}
}

func TestAllSweepStates_Empty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	got := s.AllSweepStates()
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
