// Package dashboard serves an opt-in web dashboard embedded in `noctra run`.
// It is enabled by setting DASHBOARD_ADDR in .env. The dashboard surfaces
// active runs, queue state, PR-iteration state, session stats, and per-ticket
// log tails. Kill/requeue actions are auth-gated via DASHBOARD_TOKEN.
package dashboard

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/state"
)

//go:embed static/index.html
var staticFS embed.FS

// Snapshot is the read-only view the pipeline exposes to the dashboard.
// The pipeline implements this interface so the dashboard never touches
// pipeline internals directly.
type Snapshot interface {
	// DashboardStatus returns the current pipeline state for the dashboard.
	DashboardStatus() Status
}

// Controller is the write interface for kill/requeue actions.
type Controller interface {
	// KillRun cancels the in-flight run for the given identifier.
	KillRun(identifier string) error
	// Requeue moves a ticket back to the trigger state/label, optionally
	// posting extraContext as a Linear comment first.
	Requeue(ctx context.Context, identifier, extraContext string) error
}

// Status is the JSON payload for the /api/status endpoint.
type Status struct {
	Active     []string                 `json:"active"`
	Killed     []string                 `json:"killed"`
	Skipped    []string                 `json:"skipped"`
	Failed     map[string]int           `json:"failed"`
	Stats      SessionStats             `json:"stats"`
	PRState    map[string]state.PRState `json:"pr_state"`
	MaxWorkers int                      `json:"max_workers"`
	Uptime     string                   `json:"uptime"`
}

// SessionStats holds the aggregate counters for the running session.
type SessionStats struct {
	TotalDispatches int `json:"total_dispatches"`
	MaxDispatches   int `json:"max_dispatches"`
	SuccessCount    int `json:"success_count"`
	FailCount       int `json:"fail_count"`
}

// Server is the dashboard HTTP server.
type Server struct {
	snap   Snapshot
	ctrl   Controller
	token  string // shared secret for mutation endpoints
	logDir string // directory containing per-ticket log files
	srv    *http.Server
}

// New creates a dashboard server. It does not start listening — call
// ListenAndServe for that.
func New(snap Snapshot, ctrl Controller, token, logDir string) *Server {
	s := &Server{
		snap:   snap,
		ctrl:   ctrl,
		token:  token,
		logDir: logDir,
	}

	mux := http.NewServeMux()

	// API endpoints — all JSON.
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/logs/{id}", s.handleLogs)

	// Log streaming (SSE).
	mux.HandleFunc("GET /api/logs/{id}/stream", s.StreamLogs)

	// Mutation endpoints — auth-gated.
	mux.HandleFunc("POST /api/kill", s.handleKill)
	mux.HandleFunc("POST /api/requeue", s.handleRequeue)

	// Static assets — the embedded SPA.
	mux.HandleFunc("GET /", s.handleIndex)

	s.srv = &http.Server{Handler: mux}
	return s
}

// ListenAndServe starts the dashboard on addr. It blocks until the server
// is shut down. Use Shutdown to stop it gracefully.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("dashboard listen: %w", err)
	}
	slog.Info("dashboard listening", "addr", ln.Addr().String())
	return s.srv.Serve(ln)
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// handleIndex serves the embedded single-page dashboard.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// handleStatus returns the current pipeline snapshot as JSON.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.snap.DashboardStatus())
}

// handleLogs returns the tail of the log file for a ticket identifier.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing ticket id"}`, http.StatusBadRequest)
		return
	}

	// Sanitise: only allow alphanumeric, dash, underscore to prevent path traversal.
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			http.Error(w, `{"error":"invalid ticket id"}`, http.StatusBadRequest)
			return
		}
	}

	logPath := filepath.Join(s.logDir, id+".log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, `{"error":"log not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"error":"cannot read log"}`, http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// Return the last 64KB of the log file.
	const tailBytes = 64 * 1024
	info, err := f.Stat()
	if err != nil {
		http.Error(w, `{"error":"cannot stat log"}`, http.StatusInternalServerError)
		return
	}
	offset := int64(0)
	if info.Size() > tailBytes {
		offset = info.Size() - tailBytes
	}

	buf := make([]byte, min(info.Size()-offset, tailBytes))
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		http.Error(w, `{"error":"read failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"id":   id,
		"tail": string(buf[:n]),
		"size": info.Size(),
	})
}

// handleKill terminates a running ticket. Auth-gated.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}

	var req struct {
		Identifier string `json:"identifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	req.Identifier = strings.TrimSpace(req.Identifier)
	if req.Identifier == "" {
		http.Error(w, `{"error":"identifier is required"}`, http.StatusBadRequest)
		return
	}

	if err := s.ctrl.KillRun(strings.ToUpper(req.Identifier)); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"status": "killed", "identifier": strings.ToUpper(req.Identifier)})
}

// handleRequeue moves a ticket back to the trigger state/label. Auth-gated.
func (s *Server) handleRequeue(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}

	var req struct {
		Identifier string `json:"identifier"`
		Context    string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	req.Identifier = strings.TrimSpace(req.Identifier)
	if req.Identifier == "" {
		http.Error(w, `{"error":"identifier is required"}`, http.StatusBadRequest)
		return
	}

	if err := s.ctrl.Requeue(r.Context(), strings.ToUpper(req.Identifier), strings.TrimSpace(req.Context)); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"status": "requeued", "identifier": strings.ToUpper(req.Identifier)})
}

// checkAuth validates the Authorization header against the configured token.
// Returns false (and writes a 401/403) when auth fails. When no token is
// configured, mutations are disabled entirely.
func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		http.Error(w, `{"error":"mutations disabled (no DASHBOARD_TOKEN configured)"}`, http.StatusForbidden)
		return false
	}
	auth := r.Header.Get("Authorization")
	got := strings.TrimPrefix(auth, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		slog.Warn("dashboard: json encode failed", "err", err)
	}
}

// StreamLogs streams the log file for a ticket as Server-Sent Events.
// Each new chunk appended to the file is sent as a "log" event. The
// stream ends when the request context is cancelled.
func (s *Server) StreamLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing ticket id"}`, http.StatusBadRequest)
		return
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			http.Error(w, `{"error":"invalid ticket id"}`, http.StatusBadRequest)
			return
		}
	}

	logPath := filepath.Join(s.logDir, id+".log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, `{"error":"log not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"error":"cannot read log"}`, http.StatusInternalServerError)
		return
	}
	defer f.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Flush headers so the client receives them immediately.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Start from current end of file.
	offset, _ := f.Seek(0, io.SeekEnd)

	// Cap per-tick reads so a fast-growing file can't cause unbounded
	// memory allocation.
	const maxReadPerTick = 256 * 1024 // 256 KB

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Buffer for an incomplete trailing line from the previous read.
	var partial string

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			info, err := f.Stat()
			if err != nil {
				return
			}

			// Handle log truncation (e.g. rotation): reset to the
			// new end so we don't read stale offsets.
			if info.Size() < offset {
				offset = info.Size()
				partial = ""
				continue
			}

			if info.Size() == offset {
				continue
			}

			toRead := info.Size() - offset
			if toRead > maxReadPerTick {
				toRead = maxReadPerTick
			}

			buf := make([]byte, toRead)
			n, readErr := f.ReadAt(buf, offset)
			if n > 0 {
				chunk := partial + string(buf[:n])
				partial = ""

				lines := strings.Split(chunk, "\n")
				// If the chunk doesn't end with a newline, the last
				// element is an incomplete line — buffer it for the
				// next tick.
				if !strings.HasSuffix(chunk, "\n") {
					partial = lines[len(lines)-1]
					lines = lines[:len(lines)-1]
				}

				for _, line := range lines {
					fmt.Fprintf(w, "data: %s\n", line)
				}
				_, _ = fmt.Fprint(w, "\n")
				flusher.Flush()
				offset += int64(n)
			}
			if readErr != nil && readErr != io.EOF {
				return
			}
		}
	}
}
