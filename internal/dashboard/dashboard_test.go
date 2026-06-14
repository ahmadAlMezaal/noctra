package dashboard

import (
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
)

// mockSnapshot implements Snapshot for testing.
type mockSnapshot struct {
	status Status
}

func (m *mockSnapshot) DashboardStatus() Status { return m.status }

// mockController implements Controller for testing.
type mockController struct {
	killed   []string
	requeued []struct{ id, ctx string }
	killErr  error
	reqErr   error
}

func (m *mockController) KillRun(id string) error {
	m.killed = append(m.killed, id)
	return m.killErr
}

func (m *mockController) Requeue(_ context.Context, id, ctx string) error {
	m.requeued = append(m.requeued, struct{ id, ctx string }{id, ctx})
	return m.reqErr
}

func newTestServer(snap Snapshot, ctrl Controller, token, logDir string) *httptest.Server {
	s := New(snap, ctrl, token, logDir)
	return httptest.NewServer(s.srv.Handler)
}

func TestHandleStatus(t *testing.T) {
	snap := &mockSnapshot{status: Status{
		Active:     []string{"ENG-42", "ENG-43"},
		Killed:     []string{},
		Skipped:    []string{"ENG-10"},
		Failed:     map[string]int{"ENG-11": 2},
		Stats:      SessionStats{TotalDispatches: 5, MaxDispatches: 10, SuccessCount: 3, FailCount: 1},
		MaxWorkers: 3,
		Uptime:     "1h30m",
	}}

	srv := newTestServer(snap, &mockController{}, "", "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q", ct)
	}

	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(status.Active) != 2 {
		t.Errorf("active: got %d, want 2", len(status.Active))
	}
	if status.Stats.SuccessCount != 3 {
		t.Errorf("success_count: got %d, want 3", status.Stats.SuccessCount)
	}
	if status.MaxWorkers != 3 {
		t.Errorf("max_workers: got %d, want 3", status.MaxWorkers)
	}
	if len(status.Skipped) != 1 || status.Skipped[0] != "ENG-10" {
		t.Errorf("skipped: got %v", status.Skipped)
	}
	if status.Failed["ENG-11"] != 2 {
		t.Errorf("failed: got %v", status.Failed)
	}
}

func TestHandleStatus_WithPRState(t *testing.T) {
	snap := &mockSnapshot{status: Status{
		Active:     []string{},
		Killed:     []string{},
		Skipped:    []string{},
		Failed:     map[string]int{},
		Stats:      SessionStats{},
		MaxWorkers: 3,
		Uptime:     "5m",
		PRState: map[string]state.PRState{
			"https://github.com/org/repo/pull/42": {
				TicketID:     "ENG-42",
				AgentBackend: "claude",
				Iterations:   2,
			},
		},
	}}

	srv := newTestServer(snap, &mockController{}, "", "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pr, ok := status.PRState["https://github.com/org/repo/pull/42"]
	if !ok {
		t.Fatal("expected PR state entry")
	}
	if pr.TicketID != "ENG-42" {
		t.Errorf("ticket_id: got %q", pr.TicketID)
	}
	if pr.Iterations != 2 {
		t.Errorf("iterations: got %d", pr.Iterations)
	}
}

func TestHandleLogs(t *testing.T) {
	logDir := t.TempDir()
	content := "--- Attempt 2026-01-01 ---\nline1\nline2\nline3\n"
	if err := os.WriteFile(filepath.Join(logDir, "ENG-42.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", logDir)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs/ENG-42")
	if err != nil {
		t.Fatalf("GET /api/logs/ENG-42: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["id"] != "ENG-42" {
		t.Errorf("id: got %v", result["id"])
	}
	tail, ok := result["tail"].(string)
	if !ok || !strings.Contains(tail, "line1") {
		t.Errorf("tail: got %q", tail)
	}
}

func TestHandleLogs_NotFound(t *testing.T) {
	logDir := t.TempDir()
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", logDir)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs/NOPE-99")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestHandleLogs_PathTraversal(t *testing.T) {
	logDir := t.TempDir()
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", logDir)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs/../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The mux won't match /api/logs/../../etc/passwd literally; Go's
	// http.ServeMux cleans paths. But the character filter also catches
	// dots and slashes. Either a 400 or 404 is acceptable.
	if resp.StatusCode == http.StatusOK {
		t.Error("expected non-200 for path traversal attempt")
	}
}

func TestHandleKill_Success(t *testing.T) {
	ctrl := &mockController{}
	srv := newTestServer(&mockSnapshot{status: Status{}}, ctrl, "secret", "")
	defer srv.Close()

	body := strings.NewReader(`{"identifier":"ENG-42"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/kill", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d (%s)", resp.StatusCode, b)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "killed" {
		t.Errorf("status: got %q", result["status"])
	}
	if len(ctrl.killed) != 1 || ctrl.killed[0] != "ENG-42" {
		t.Errorf("killed: got %v", ctrl.killed)
	}
}

func TestHandleKill_NoToken(t *testing.T) {
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", "")
	defer srv.Close()

	body := strings.NewReader(`{"identifier":"ENG-42"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/kill", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 when no token configured, got %d", resp.StatusCode)
	}
}

func TestHandleKill_WrongToken(t *testing.T) {
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "secret", "")
	defer srv.Close()

	body := strings.NewReader(`{"identifier":"ENG-42"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/kill", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token, got %d", resp.StatusCode)
	}
}

func TestHandleKill_Error(t *testing.T) {
	ctrl := &mockController{killErr: fmt.Errorf("no active run for ENG-99")}
	srv := newTestServer(&mockSnapshot{status: Status{}}, ctrl, "secret", "")
	defer srv.Close()

	body := strings.NewReader(`{"identifier":"ENG-99"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/kill", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["error"] == "" {
		t.Error("expected error in response")
	}
}

func TestHandleRequeue_Success(t *testing.T) {
	ctrl := &mockController{}
	srv := newTestServer(&mockSnapshot{status: Status{}}, ctrl, "tok", "")
	defer srv.Close()

	body := strings.NewReader(`{"identifier":"ENG-42","context":"try Auth0"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/requeue", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d (%s)", resp.StatusCode, b)
	}

	if len(ctrl.requeued) != 1 {
		t.Fatalf("expected 1 requeue, got %d", len(ctrl.requeued))
	}
	if ctrl.requeued[0].id != "ENG-42" {
		t.Errorf("id: got %q", ctrl.requeued[0].id)
	}
	if ctrl.requeued[0].ctx != "try Auth0" {
		t.Errorf("ctx: got %q", ctrl.requeued[0].ctx)
	}
}

func TestHandleRequeue_NoAuth(t *testing.T) {
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "tok", "")
	defer srv.Close()

	body := strings.NewReader(`{"identifier":"ENG-42"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/requeue", body)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHandleIndex(t *testing.T) {
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type: got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Noctra Dashboard") {
		t.Error("expected 'Noctra Dashboard' in HTML body")
	}
}

func TestHandleIndex_NotFoundForOtherPaths(t *testing.T) {
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", "")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for /nonexistent, got %d", resp.StatusCode)
	}
}

func TestHandleLogs_LargeFile(t *testing.T) {
	logDir := t.TempDir()
	// Create a file larger than 64KB.
	data := strings.Repeat("x", 100*1024)
	if err := os.WriteFile(filepath.Join(logDir, "ENG-99.log"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", logDir)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs/ENG-99")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	tail, ok := result["tail"].(string)
	if !ok {
		t.Fatal("expected tail field")
	}
	// Should return at most 64KB (the tail).
	if len(tail) > 65*1024 {
		t.Errorf("tail too large: %d bytes", len(tail))
	}
	if len(tail) < 60*1024 {
		t.Errorf("tail too small: %d bytes", len(tail))
	}
}

func TestHandleKill_MissingIdentifier(t *testing.T) {
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "tok", "")
	defer srv.Close()

	body := strings.NewReader(`{}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/kill", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing identifier, got %d", resp.StatusCode)
	}
}

func TestStreamLogs(t *testing.T) {
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "ENG-50.log")
	if err := os.WriteFile(logFile, []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "", logDir)
	defer srv.Close()

	// Use a short-lived context so the SSE stream eventually closes.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/logs/ENG-50/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	// Append to the log file after a short delay (so the stream is connected).
	go func() {
		time.Sleep(1500 * time.Millisecond)
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		f.WriteString("streamed line\n")
		f.Close()
	}()

	// Read until context deadline; collect everything we get.
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "streamed line") {
		t.Errorf("expected 'streamed line' in SSE output, got %q", string(body))
	}
}

func TestCheckAuth_ConstantTimeCompare(t *testing.T) {
	// Verify that auth passes with correct token and fails with wrong.
	srv := newTestServer(&mockSnapshot{status: Status{}}, &mockController{}, "mytoken", "")
	defer srv.Close()

	// Correct token.
	body := strings.NewReader(`{"identifier":"ENG-1"}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/kill", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer mytoken")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// KillRun will error (no active run) but auth should pass (not 401/403).
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Error("expected auth to pass with correct token")
	}

	// Wrong token.
	body = strings.NewReader(`{"identifier":"ENG-1"}`)
	req, _ = http.NewRequest("POST", srv.URL+"/api/kill", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", resp.StatusCode)
	}
}
