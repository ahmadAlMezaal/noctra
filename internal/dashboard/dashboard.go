// Package dashboard serves a read-only HTTP dashboard showing a point-in-time
// snapshot of the pipeline. All requests require a valid dashboard token.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
	"github.com/ahmadAlMezaal/noctra/internal/state"
	"github.com/ahmadAlMezaal/noctra/internal/sweep"
)

//go:embed static
var staticFiles embed.FS

// SnapshotFunc returns the current pipeline snapshot as JSON-serializable data.
type SnapshotFunc func() any

// Providers supplies the data sources the dashboard reads from. All fields
// are optional — a nil Store or empty function gracefully degrades the
// corresponding panel to an empty response.
type Providers struct {
	Store           *state.Store
	SweepTasks      []sweep.Task
	MaxPRIterations int
	RepoPaths       func() []string // repo.Resolver.AllRepoPaths
	LogDir          string
	Hub             *Hub
}

// Server is the dashboard HTTP server.
type Server struct {
	srv        *http.Server
	token      string
	snapshotFn SnapshotFunc
	prov       Providers
}

// New creates a dashboard server bound to addr, gated by the given token.
// snapshotFn is called on each GET /api/snapshot to produce the response payload.
// prov supplies optional data sources for history, cost, PR, and sweep panels.
func New(addr, token string, snapshotFn SnapshotFunc, prov Providers) *Server {
	mux := http.NewServeMux()

	s := &Server{
		srv: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
		token:      token,
		snapshotFn: snapshotFn,
		prov:       prov,
	}
	if s.prov.Hub == nil {
		s.prov.Hub = NewHub(defaultMaxSubscribers)
	}

	mux.Handle("/api/snapshot", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshotFn())
	})))

	mux.Handle("/api/events", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleEvents(w, r)
	})))

	mux.Handle("/api/logs/", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleLogs(w, r)
	})))

	mux.Handle("/api/history", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleHistory(w, r, prov.Store)
	})))

	mux.Handle("/api/prs", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handlePRs(w, r, prov.Store, prov.MaxPRIterations)
	})))

	mux.Handle("/api/sweeps", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSweeps(w, r, prov.Store, prov.SweepTasks)
	})))

	mux.Handle("/api/cost", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleCost(w, r, prov.Store)
	})))

	staticSub, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("/", s.requireAuth(fileServer))

	return s
}

// ListenAndServe starts listening. It blocks until the server is shut down.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	slog.Info("dashboard listening", "addr", ln.Addr().String())
	return s.srv.Serve(ln)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// Handler returns the server's HTTP handler (for testing with httptest).
func (s *Server) Handler() http.Handler {
	return s.srv.Handler
}

// requireAuth returns middleware that checks for a valid Bearer token in the
// Authorization header, or a ?token= query parameter (convenience for browser
// page loads).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Hub returns the server's live event hub.
func (s *Server) Hub() *Hub {
	return s.prov.Hub
}

// ── API handlers ────────────────────────────────────────────────────────────

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	events, unsubscribe, ok := s.prov.Hub.Subscribe(r.Context())
	if !ok {
		http.Error(w, "too many subscribers", http.StatusTooManyRequests)
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if !s.writeSnapshotEvent(w) {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			if !s.writeSnapshotEvent(w) {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) writeSnapshotEvent(w http.ResponseWriter) bool {
	b, err := json.Marshal(s.snapshotFn())
	if err != nil {
		slog.Warn("dashboard snapshot encode failed", "err", err)
		return false
	}
	if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b); err != nil {
		return false
	}
	return true
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	if id == "" || id != filepath.Base(id) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	logFile := filepath.Join(s.prov.LogDir, id+".log")
	if r.URL.Query().Get("follow") != "1" && r.URL.Query().Get("follow") != "true" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, boundedTail(logFile, 64*1024))
		return
	}
	s.followLog(w, r, logFile)
}

func (s *Server) followLog(w http.ResponseWriter, r *http.Request, logFile string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	offset := agent.OffsetBefore(logFile)
	initial := boundedTail(logFile, 64*1024)
	if initial != "" {
		writeLogEvent(w, initial)
		flusher.Flush()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			next := agent.OffsetBefore(logFile)
			if next <= offset {
				continue
			}
			chunk := agent.ReadAfter(logFile, offset)
			offset = next
			if chunk == "" {
				continue
			}
			if len(chunk) > 32*1024 {
				chunk = chunk[len(chunk)-32*1024:]
			}
			writeLogEvent(w, chunk)
			flusher.Flush()
		}
	}
}

func boundedTail(logFile string, maxBytes int64) string {
	size := agent.OffsetBefore(logFile)
	if size <= 0 {
		return ""
	}
	offset := size - maxBytes
	if offset < 0 {
		offset = 0
	}
	return agent.ReadAfter(logFile, offset)
}

func writeLogEvent(w http.ResponseWriter, chunk string) {
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
}

type historyEntry struct {
	Identifier string  `json:"identifier"`
	TicketID   string  `json:"ticket_id,omitempty"`
	PRURL      string  `json:"pr_url,omitempty"`
	Repo       string  `json:"repo"`
	RunType    string  `json:"run_type"`
	Status     string  `json:"status"`
	DurationS  float64 `json:"duration_s"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at,omitempty"`
	LinearURL  string  `json:"linear_url,omitempty"`
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, store *state.Store) {
	if store == nil {
		writeJSON(w, []historyEntry{})
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	runs, err := store.ListRunHistory(limit)
	if err != nil {
		slog.Warn("dashboard history query failed", "err", err)
		writeJSON(w, []historyEntry{})
		return
	}
	entries := make([]historyEntry, 0, len(runs))
	for _, r := range runs {
		dur := 0.0
		if !r.FinishedAt.IsZero() && !r.StartedAt.IsZero() {
			dur = r.FinishedAt.Sub(r.StartedAt).Seconds()
		}
		e := historyEntry{
			Identifier: r.Identifier,
			TicketID:   r.TicketID,
			PRURL:      r.PRURL,
			Repo:       r.Repo,
			RunType:    r.RunType,
			Status:     r.Status,
			DurationS:  dur,
			StartedAt:  r.StartedAt.UTC().Format(time.RFC3339),
		}
		if !r.FinishedAt.IsZero() {
			e.FinishedAt = r.FinishedAt.UTC().Format(time.RFC3339)
		}
		if r.TicketID != "" {
			e.LinearURL = linearURL(r.TicketID)
		}
		entries = append(entries, e)
	}
	writeJSON(w, entries)
}

type prEntry struct {
	PRURL         string `json:"pr_url"`
	TicketID      string `json:"ticket_id,omitempty"`
	Iterations    int    `json:"iterations"`
	MaxIterations int    `json:"max_iterations"`
	Capped        bool   `json:"capped"`
	LastCISHA     string `json:"last_ci_sha,omitempty"`
	LastCIRunURL  string `json:"last_ci_run_url,omitempty"`
	LastReasoning string `json:"last_reasoning,omitempty"`
	LinearURL     string `json:"linear_url,omitempty"`
}

func (s *Server) handlePRs(w http.ResponseWriter, _ *http.Request, store *state.Store, maxIter int) {
	if store == nil {
		writeJSON(w, []prEntry{})
		return
	}
	all := store.All()
	entries := make([]prEntry, 0, len(all))
	for url, pr := range all {
		e := prEntry{
			PRURL:         url,
			TicketID:      pr.TicketID,
			Iterations:    pr.Iterations,
			MaxIterations: maxIter,
			Capped:        maxIter > 0 && pr.Iterations >= maxIter,
			LastCISHA:     pr.LastCISHA,
			LastCIRunURL:  pr.LastCIRunURL,
			LastReasoning: pr.LastReasoning,
		}
		if pr.TicketID != "" {
			e.LinearURL = linearURL(pr.TicketID)
		}
		entries = append(entries, e)
	}
	writeJSON(w, entries)
}

type sweepEntry struct {
	Repo          string  `json:"repo"`
	Task          string  `json:"task"`
	Description   string  `json:"description"`
	CooldownH     float64 `json:"cooldown_h"`
	LastRunAt     string  `json:"last_run_at,omitempty"`
	CooldownLeftH float64 `json:"cooldown_left_h"`
	Eligible      bool    `json:"eligible"`
}

func (s *Server) handleSweeps(w http.ResponseWriter, _ *http.Request, store *state.Store, tasks []sweep.Task) {
	if store == nil || len(tasks) == 0 {
		writeJSON(w, []sweepEntry{})
		return
	}
	sweepStates := store.AllSweepStates()
	now := time.Now()
	var entries []sweepEntry
	for key, ss := range sweepStates {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		repoSlug, taskName := parts[0], parts[1]
		var task *sweep.Task
		for i := range tasks {
			if tasks[i].Name == taskName {
				task = &tasks[i]
				break
			}
		}
		desc := ""
		cooldown := time.Duration(0)
		if task != nil {
			desc = task.Description
			cooldown = task.Cooldown
		}
		cooldownLeft := time.Duration(0)
		eligible := true
		if !ss.LastRunAt.IsZero() && cooldown > 0 {
			nextEligible := ss.LastRunAt.Add(cooldown)
			if now.Before(nextEligible) {
				cooldownLeft = nextEligible.Sub(now)
				eligible = false
			}
		}
		e := sweepEntry{
			Repo:          repoSlug,
			Task:          taskName,
			Description:   desc,
			CooldownH:     cooldown.Hours(),
			CooldownLeftH: cooldownLeft.Hours(),
			Eligible:      eligible,
		}
		if !ss.LastRunAt.IsZero() {
			e.LastRunAt = ss.LastRunAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, e)
	}
	writeJSON(w, entries)
}

type costBucket struct {
	Date        string  `json:"date"`
	CostUSD     float64 `json:"cost_usd"`
	TotalTokens int64   `json:"total_tokens"`
}

type costResponse struct {
	Buckets []costBucket `json:"buckets"`
}

func (s *Server) handleCost(w http.ResponseWriter, r *http.Request, store *state.Store) {
	if store == nil {
		writeJSON(w, costResponse{})
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	since := time.Now().UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour)
	events, err := store.ListUsageEvents(since)
	if err != nil {
		slog.Warn("dashboard cost query failed", "err", err)
		writeJSON(w, costResponse{})
		return
	}

	bucketMap := map[string]*costBucket{}
	for _, ev := range events {
		day := ev.OccurredAt.UTC().Format("2006-01-02")
		b, ok := bucketMap[day]
		if !ok {
			b = &costBucket{Date: day}
			bucketMap[day] = b
		}
		b.CostUSD += ev.CostUSD
		b.TotalTokens += ev.TotalTokens
	}

	buckets := make([]costBucket, 0, len(bucketMap))
	for d := since; !d.After(time.Now().UTC()); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		if b, ok := bucketMap[day]; ok {
			buckets = append(buckets, *b)
		} else {
			buckets = append(buckets, costBucket{Date: day})
		}
	}
	writeJSON(w, costResponse{Buckets: buckets})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func linearURL(ticketID string) string {
	parts := strings.SplitN(ticketID, "-", 2)
	if len(parts) != 2 {
		return ""
	}
	return "https://linear.app/issue/" + ticketID
}
