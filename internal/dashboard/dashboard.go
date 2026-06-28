// Package dashboard serves the HTTP pipeline-snapshot dashboard: reads require the dashboard token, mutating controls the admin token.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
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

type SnapshotFunc func() any

// Controls defines the mutating operations the dashboard can invoke on the pipeline.
type Controls interface {
	KillRun(identifier string) error
	RequeueTicket(ctx context.Context, identifier, extraContext, source string) error
	PauseDispatch() bool
	ResumeDispatch() bool
	ClearSkipped(identifier string) error
}

// Providers supplies the dashboard's data sources; all fields optional — nil degrades to empty responses.
type Providers struct {
	Store           *state.Store
	SweepTasks      []sweep.Task
	MaxPRIterations int
	RepoPaths       func() []string // repo.Resolver.AllRepoPaths
	LogDir          string
	Hub             *Hub
}

type Server struct {
	srv        *http.Server
	token      string
	adminToken string
	snapshotFn SnapshotFunc
	prov       Providers
	controls   Controls
	redactor   *Redactor
}

func New(addr, token, adminToken string, snapshotFn SnapshotFunc, prov Providers, ctrl Controls, redactor *Redactor) *Server {
	mux := http.NewServeMux()

	s := &Server{
		srv: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
		token:      token,
		adminToken: adminToken,
		snapshotFn: snapshotFn,
		prov:       prov,
		controls:   ctrl,
		redactor:   redactor,
	}
	if s.prov.Hub == nil {
		s.prov.Hub = NewHub(defaultMaxSubscribers)
	}

	// ── Read endpoints (read token) ─────────────────────────────────────

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

	mux.Handle("/api/spend", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSpend(w, r, prov.Store)
	})))

	mux.Handle("/api/admin-status", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]bool{"admin_enabled": s.adminToken != ""})
	})))

	// ── Control endpoints (admin token) ─────────────────────────────────

	mux.Handle("/api/kill/", s.requireAdmin(http.HandlerFunc(s.handleKill)))
	mux.Handle("/api/requeue/", s.requireAdmin(http.HandlerFunc(s.handleRequeue)))
	mux.Handle("/api/pause", s.requireAdmin(http.HandlerFunc(s.handlePause)))
	mux.Handle("/api/resume", s.requireAdmin(http.HandlerFunc(s.handleResume)))
	mux.Handle("/api/retry/", s.requireAdmin(http.HandlerFunc(s.handleRetry)))

	// ── Static files ────────────────────────────────────────────────────

	staticSub, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	// Fonts are public (no secrets) and must load unauthenticated: @font-face url() subrequests don't carry the page's ?token=, so gating them silently breaks the brand fonts.
	mux.Handle("/fonts/", fileServer)
	mux.Handle("/", s.requireAuth(fileServer))

	return s
}

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	slog.Info("dashboard listening", "addr", ln.Addr().String())
	return s.srv.Serve(ln)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) Handler() http.Handler {
	return s.srv.Handler
}

// requireAuth accepts either the read or admin token via header or ?token= query param.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if token != s.token && (s.adminToken == "" || token != s.adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAdmin enforces admin token via header only (no query param) for CSRF safety.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.adminToken == "" {
			http.Error(w, "admin controls not configured", http.StatusForbidden)
			return
		}
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token != s.adminToken {
			if token == s.token {
				http.Error(w, "admin token required", http.StatusForbidden)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func (s *Server) Hub() *Hub {
	return s.prov.Hub
}

// ── Read API handlers ───────────────────────────────────────────────────────

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
		content := boundedTail(logFile, 64*1024)
		_, _ = fmt.Fprint(w, s.redactor.Redact(content))
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
		writeLogEvent(w, s.redactor.Redact(initial))
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
			writeLogEvent(w, s.redactor.Redact(chunk))
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
	Agent      string  `json:"agent_backend,omitempty"`
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
			Agent:      r.AgentBackend,
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

// ── Control API handlers (admin-gated) ──────────────────────────────────────

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/kill/")
	if id == "" {
		http.Error(w, "missing identifier", http.StatusBadRequest)
		return
	}
	if s.controls == nil {
		http.Error(w, "controls not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.controls.KillRun(id); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("dashboard: killed run", "id", id)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleRequeue(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/requeue/")
	if id == "" {
		http.Error(w, "missing identifier", http.StatusBadRequest)
		return
	}
	if s.controls == nil {
		http.Error(w, "controls not available", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Context string `json:"context"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		limited := io.LimitReader(r.Body, 4096)
		_ = json.NewDecoder(limited).Decode(&body)
	}
	if err := s.controls.RequeueTicket(r.Context(), id, body.Context, "Dashboard"); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("dashboard: requeued ticket", "id", id)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if s.controls == nil {
		http.Error(w, "controls not available", http.StatusServiceUnavailable)
		return
	}
	already := s.controls.PauseDispatch()
	slog.Info("dashboard: paused dispatch", "already_paused", already)
	writeJSON(w, map[string]any{"status": "ok", "already_paused": already})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if s.controls == nil {
		http.Error(w, "controls not available", http.StatusServiceUnavailable)
		return
	}
	wasPaused := s.controls.ResumeDispatch()
	slog.Info("dashboard: resumed dispatch", "was_paused", wasPaused)
	writeJSON(w, map[string]any{"status": "ok", "was_paused": wasPaused})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/retry/")
	if id == "" {
		http.Error(w, "missing identifier", http.StatusBadRequest)
		return
	}
	if s.controls == nil {
		http.Error(w, "controls not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.controls.ClearSkipped(id); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("dashboard: cleared skipped", "id", id)
	writeJSON(w, map[string]string{"status": "ok"})
}

type spendEntry struct {
	Agent       string  `json:"agent"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// handleSpend aggregates usage events into per-agent token/cost totals; window defaults to the current UTC day, widened by ?days=N.
func (s *Server) handleSpend(w http.ResponseWriter, r *http.Request, store *state.Store) {
	if store == nil {
		writeJSON(w, []spendEntry{})
		return
	}
	since := time.Now().UTC().Truncate(24 * time.Hour)
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			since = time.Now().UTC().AddDate(0, 0, -n).Truncate(24 * time.Hour)
		}
	}
	events, err := store.ListUsageEvents(since)
	if err != nil {
		slog.Warn("dashboard spend query failed", "err", err)
		writeJSON(w, []spendEntry{})
		return
	}

	byAgent := map[string]*spendEntry{}
	order := []string{}
	for _, ev := range events {
		agentName := ev.AgentBackend
		if agentName == "" {
			agentName = "unknown"
		}
		e, ok := byAgent[agentName]
		if !ok {
			e = &spendEntry{Agent: agentName}
			byAgent[agentName] = e
			order = append(order, agentName)
		}
		e.TotalTokens += ev.TotalTokens
		e.CostUSD += ev.CostUSD
	}

	entries := make([]spendEntry, 0, len(order))
	for _, name := range order {
		entries = append(entries, *byAgent[name])
	}
	writeJSON(w, entries)
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
