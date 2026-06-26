package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	return New(":0", token, "", func() any {
		return testSnapshot{Active: []string{"ENG-1"}, OK: true}
	}, Providers{}, nil, nil)
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

func TestEvents_InitialSnapshotFrame(t *testing.T) {
	s := newTestServer("tok")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events?token=tok")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if line == "\n" || err == io.EOF {
			break
		}
		frame.WriteString(line)
	}
	got := frame.String()
	if !strings.Contains(got, "event: snapshot\n") {
		t.Fatalf("missing snapshot event: %q", got)
	}
	if !strings.Contains(got, `"active":["ENG-1"]`) {
		t.Fatalf("missing initial snapshot data: %q", got)
	}
}

func TestEvents_AuthRequired(t *testing.T) {
	s := newTestServer("tok")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLogsFollow_InitialTailFrameAndAuth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ENG-9.log"), []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(":0", "tok", "", func() any { return nil }, Providers{LogDir: dir}, nil, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/logs/ENG-9?follow=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/api/logs/ENG-9?follow=1&token=tok")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if line == "\n" || err == io.EOF {
			break
		}
		frame.WriteString(line)
	}
	got := frame.String()
	if !strings.Contains(got, "event: log\n") || !strings.Contains(got, `line two\n`) {
		t.Fatalf("unexpected log frame: %q", got)
	}
}

// ── History endpoint tests (ENG-277) ────────────────────────────────────────

func newTestServerWithStore(t *testing.T, token string) (*Server, *state.Store) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(":0", token, "", func() any { return nil }, Providers{
		Store:           store,
		MaxPRIterations: 3,
		SweepTasks: []sweep.Task{
			{Name: "lint-cleanup", Description: "Fix lint warnings", Cooldown: 7 * 24 * time.Hour},
		},
	}, nil, nil)
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

func authedPost(t *testing.T, ts *httptest.Server, path, token string, body string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, _ := http.NewRequest("POST", ts.URL+path, bodyReader)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHistory_ReturnsRuns(t *testing.T) {
	srv, store := newTestServerWithStore(t, "tok")
	defer func() { _ = store.Close() }()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.InsertRunHistory(state.RunHistory{
		Identifier: "ENG-1", TicketID: "ENG-1", Repo: "my-repo",
		RunType: "ticket", Status: "pr_opened",
		PRURL:     "https://github.com/o/r/pull/1",
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
	srv := New(":0", "tok", "", func() any { return nil }, Providers{}, nil, nil)
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
	defer func() { _ = store.Close() }()
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
	defer func() { _ = store.Close() }()
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
	defer func() { _ = store.Close() }()
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
	defer func() { _ = store.Close() }()
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

// ── Admin auth / control endpoint tests (ENG-276) ───────────────────────────

// stubControls implements Controls for testing.
type stubControls struct {
	killErr     error
	requeueErr  error
	retryErr    error
	pauseCalled bool
	resumeCall  bool
}

func (s *stubControls) KillRun(id string) error      { return s.killErr }
func (s *stubControls) PauseDispatch() bool          { s.pauseCalled = true; return false }
func (s *stubControls) ResumeDispatch() bool         { s.resumeCall = true; return true }
func (s *stubControls) ClearSkipped(id string) error { return s.retryErr }
func (s *stubControls) RequeueTicket(_ context.Context, id, ctx, src string) error {
	return s.requeueErr
}

func TestAdminGating_KillEndpoint(t *testing.T) {
	ctrl := &stubControls{}
	srv := New(":0", "read-tok", "admin-tok", func() any { return nil }, Providers{}, ctrl, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Admin token → 200
	resp := authedPost(t, ts, "/api/kill/ENG-1", "admin-tok", "")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("admin token: expected 200, got %d", resp.StatusCode)
	}

	// Read token → 403
	resp = authedPost(t, ts, "/api/kill/ENG-1", "read-tok", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("read token: expected 403, got %d", resp.StatusCode)
	}

	// No token → 401
	req, _ := http.NewRequest("POST", ts.URL+"/api/kill/ENG-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: expected 401, got %d", resp.StatusCode)
	}

	// Wrong token → 401
	resp = authedPost(t, ts, "/api/kill/ENG-1", "wrong", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: expected 401, got %d", resp.StatusCode)
	}

	// GET → 405
	resp = authedGet(t, ts, "/api/kill/ENG-1", "admin-tok")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: expected 405, got %d", resp.StatusCode)
	}
}

func TestAdminGating_NoAdminToken(t *testing.T) {
	ctrl := &stubControls{}
	srv := New(":0", "read-tok", "", func() any { return nil }, Providers{}, ctrl, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := authedPost(t, ts, "/api/kill/ENG-1", "read-tok", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 when admin token unset, got %d", resp.StatusCode)
	}
}

func TestAdminGating_PauseResume(t *testing.T) {
	ctrl := &stubControls{}
	srv := New(":0", "read-tok", "admin-tok", func() any { return nil }, Providers{}, ctrl, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := authedPost(t, ts, "/api/pause", "admin-tok", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("pause: expected 200, got %d", resp.StatusCode)
	}

	resp2 := authedPost(t, ts, "/api/resume", "admin-tok", "")
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("resume: expected 200, got %d", resp2.StatusCode)
	}
}

func TestAdminGating_RetryEndpoint(t *testing.T) {
	ctrl := &stubControls{}
	srv := New(":0", "read-tok", "admin-tok", func() any { return nil }, Providers{}, ctrl, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := authedPost(t, ts, "/api/retry/ENG-1", "admin-tok", "")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// With error
	ctrl.retryErr = fmt.Errorf("not in skipped set")
	resp = authedPost(t, ts, "/api/retry/ENG-2", "admin-tok", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 (error in body), got %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["error"] == "" {
		t.Error("expected error in response body")
	}
}

func TestAdminGating_RequeueWithContext(t *testing.T) {
	ctrl := &stubControls{}
	srv := New(":0", "read-tok", "admin-tok", func() any { return nil }, Providers{}, ctrl, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := authedPost(t, ts, "/api/requeue/ENG-1", "admin-tok", `{"context":"use Auth0"}`)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAdminStatus_Endpoint(t *testing.T) {
	// Admin configured
	srv := New(":0", "read-tok", "admin-tok", func() any { return nil }, Providers{}, nil, nil)
	ts := httptest.NewServer(srv.Handler())

	resp := authedGet(t, ts, "/api/admin-status", "read-tok")
	defer resp.Body.Close()
	var status map[string]bool
	_ = json.NewDecoder(resp.Body).Decode(&status)
	if !status["admin_enabled"] {
		t.Error("expected admin_enabled=true")
	}
	ts.Close()

	// Admin not configured
	srv2 := New(":0", "read-tok", "", func() any { return nil }, Providers{}, nil, nil)
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()

	resp2 := authedGet(t, ts2, "/api/admin-status", "read-tok")
	defer resp2.Body.Close()
	var status2 map[string]bool
	_ = json.NewDecoder(resp2.Body).Decode(&status2)
	if status2["admin_enabled"] {
		t.Error("expected admin_enabled=false")
	}
}
