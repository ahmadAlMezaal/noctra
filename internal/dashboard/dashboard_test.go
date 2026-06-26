package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/state"
	"github.com/ahmadAlMezaal/noctra/internal/sweep"
)

type testSnapshot struct {
	Active []string `json:"active"`
	OK     bool     `json:"ok"`
}

func newTestServer(token string) *Server {
	return New(":0", token, func() any {
		return testSnapshot{Active: []string{"ENG-1"}, OK: true}
	}, Providers{})
}

func TestAuth_BearerToken(t *testing.T) {
	s := newTestServer("secret-token")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var snap testSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if !snap.OK || len(snap.Active) != 1 || snap.Active[0] != "ENG-1" {
		t.Errorf("unexpected snapshot: %+v", snap)
	}
}

func TestAuth_QueryParam(t *testing.T) {
	s := newTestServer("qp-token")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/snapshot?token=qp-token")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAuth_Missing(t *testing.T) {
	s := newTestServer("secret")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuth_WrongToken(t *testing.T) {
	s := newTestServer("correct")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuth_StaticPage(t *testing.T) {
	s := newTestServer("page-token")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 for page without token, got %d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/?token=page-token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 for page with token, got %d", resp.StatusCode)
	}
}

func TestSnapshot_MethodNotAllowed(t *testing.T) {
	s := newTestServer("tok")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// ── History endpoint tests (ENG-277) ────────────────────────────────────────

func newTestServerWithStore(t *testing.T, token string) (*Server, *state.Store) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(":0", token, func() any { return nil }, Providers{
		Store:           store,
		MaxPRIterations: 3,
		SweepTasks: []sweep.Task{
			{Name: "lint-cleanup", Description: "Fix lint warnings", Cooldown: 7 * 24 * time.Hour},
		},
	})
	return srv, store
}

func authedGet(t *testing.T, ts *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHistory_ReturnsRuns(t *testing.T) {
	srv, store := newTestServerWithStore(t, "tok")
	defer store.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.InsertRunHistory(state.RunHistory{
		Identifier: "ENG-1", TicketID: "ENG-1", Repo: "my-repo",
		RunType: "ticket", Status: "pr_opened",
		PRURL:    "https://github.com/o/r/pull/1",
		StartedAt: now.Add(-5 * time.Minute), FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	resp := authedGet(t, ts, "/api/history?limit=10", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var entries []historyEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Identifier != "ENG-1" {
		t.Errorf("identifier: got %q", entries[0].Identifier)
	}
	if entries[0].PRURL != "https://github.com/o/r/pull/1" {
		t.Errorf("pr_url: got %q", entries[0].PRURL)
	}
	if entries[0].DurationS < 299 || entries[0].DurationS > 301 {
		t.Errorf("duration_s: got %f, want ~300", entries[0].DurationS)
	}
	if entries[0].LinearURL == "" {
		t.Error("expected linear_url to be set")
	}
}

func TestHistory_NilStore(t *testing.T) {
	srv := New(":0", "tok", func() any { return nil }, Providers{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := authedGet(t, ts, "/api/history", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var entries []historyEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty, got %d", len(entries))
	}
}

func TestPRs_ReturnsTrackedPRs(t *testing.T) {
	srv, store := newTestServerWithStore(t, "tok")
	defer store.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	prURL := "https://github.com/o/r/pull/5"
	if err := store.Update(prURL, func(r *state.PRState) {
		r.TicketID = "ENG-5"
		r.Iterations = 2
		r.LastCISHA = "abc1234"
		r.LastReasoning = "Fixed the bug"
	}); err != nil {
		t.Fatal(err)
	}

	resp := authedGet(t, ts, "/api/prs", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var entries []prEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].PRURL != prURL {
		t.Errorf("pr_url: got %q", entries[0].PRURL)
	}
	if entries[0].Iterations != 2 {
		t.Errorf("iterations: got %d", entries[0].Iterations)
	}
	if entries[0].MaxIterations != 3 {
		t.Errorf("max_iterations: got %d", entries[0].MaxIterations)
	}
	if entries[0].Capped {
		t.Error("expected not capped")
	}
	if entries[0].LastReasoning != "Fixed the bug" {
		t.Errorf("last_reasoning: got %q", entries[0].LastReasoning)
	}
}

func TestSweeps_ReturnsSweepState(t *testing.T) {
	srv, store := newTestServerWithStore(t, "tok")
	defer store.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.UpdateSweep("my-repo/lint-cleanup", func(ss *state.SweepState) {
		ss.LastRunAt = now
	}); err != nil {
		t.Fatal(err)
	}

	resp := authedGet(t, ts, "/api/sweeps", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var entries []sweepEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Repo != "my-repo" {
		t.Errorf("repo: got %q", entries[0].Repo)
	}
	if entries[0].Task != "lint-cleanup" {
		t.Errorf("task: got %q", entries[0].Task)
	}
	if entries[0].Eligible {
		t.Error("expected not eligible (just ran)")
	}
}

func TestCost_ReturnsBuckets(t *testing.T) {
	srv, store := newTestServerWithStore(t, "tok")
	defer store.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	now := time.Now().UTC()
	if err := store.RecordUsage(state.UsageEvent{
		OccurredAt: now, Source: "ticket", CostUSD: 0.50, TotalTokens: 10000,
	}); err != nil {
		t.Fatal(err)
	}

	resp := authedGet(t, ts, "/api/cost?days=7", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var cr costResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.Buckets) == 0 {
		t.Fatal("expected non-empty buckets")
	}
	// At least one bucket should have cost > 0
	found := false
	for _, b := range cr.Buckets {
		if b.CostUSD > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one bucket with cost > 0")
	}
}

func TestHistory_MethodNotAllowed(t *testing.T) {
	srv, store := newTestServerWithStore(t, "tok")
	defer store.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/history", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestLinearURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"ENG-42", "https://linear.app/issue/ENG-42"},
		{"PROJ-1", "https://linear.app/issue/PROJ-1"},
		{"nohyphen", ""},
	}
	for _, tc := range tests {
		got := linearURL(tc.in)
		if got != tc.want {
			t.Errorf("linearURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
