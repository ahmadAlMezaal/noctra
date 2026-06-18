// Package state persists Noctra's durable runtime state.
//
// The store tracks PR review/CI cursors, autonomous sweep cooldowns, and the
// first history-oriented tables used by budget/run analytics. It is backed by
// SQLite so future features can query historical data without replacing this
// package again.
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// PRState is the per-PR record stored in the state database.
type PRState struct {
	// TicketID is the Linear identifier the PR was opened for (e.g. ENG-42).
	// Used to route comments on Linear when re-engaging or capping.
	TicketID string `json:"ticket_id,omitempty"`

	// AgentBackend is the coding-agent backend the PR was created with
	// (e.g. "claude", "codex", "copilot"). Persisted so the auto-iterate
	// path uses the same backend for follow-up commits on this PR.
	AgentBackend string `json:"agent_backend,omitempty"`

	// LastCommentAt is the createdAt of the most recent issue-conversation
	// comment the watcher has already processed for this PR. Anything with
	// a later timestamp is "new" and worth acting on. Tracking by timestamp
	// rather than ID because `gh` returns GraphQL node IDs (strings without
	// natural ordering), whereas timestamps sort cleanly.
	LastCommentAt time.Time `json:"last_comment_at,omitempty"`

	// LastReviewAt is the submittedAt of the most recent review already
	// processed.
	LastReviewAt time.Time `json:"last_review_at,omitempty"`

	// LastCISHA is the head commit SHA whose failing CI Noctra has
	// already re-engaged on. CI is keyed by SHA (not timestamp) so a failure
	// is acted on once per commit; pushing a fix changes the SHA, making a
	// fresh failure eligible again — bounded by Iterations.
	LastCISHA string `json:"last_ci_sha,omitempty"`

	// Iterations counts how many times Noctra has re-engaged on this
	// PR. Capped by config.MaxPRIterations.
	Iterations int `json:"iterations,omitempty"`

	// LastIteratedAt is the timestamp of the most recent re-engage. Mostly
	// for telemetry — also useful for spotting stuck iteration loops.
	LastIteratedAt time.Time `json:"last_iterated_at,omitempty"`
}

// SweepState is the per-task-per-repo record for autonomous maintenance
// sweeps (ENG-222). The key is "repo-slug/task-name".
type SweepState struct {
	// LastRunAt is when this task last ran on this repo.
	LastRunAt time.Time `json:"last_run_at,omitempty"`
}

type fileFormat struct {
	PRs    map[string]*PRState    `json:"prs"`
	Sweeps map[string]*SweepState `json:"sweeps,omitempty"`
}

// Store is a thread-safe SQLite-backed PR and sweep state store.
type Store struct {
	mu   sync.Mutex
	path string
	db   *sql.DB
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// Open opens the SQLite state database at path. If legacyJSONPath is provided,
// an existing JSON state file is imported once after the schema is ready. A
// missing database is not an error; it is created on first open.
func Open(path string, legacyJSONPath ...string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite state %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{path: path, db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	if len(legacyJSONPath) > 0 && legacyJSONPath[0] != "" {
		if err := s.migrateJSON(legacyJSONPath[0]); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) init() error {
	stmts := []string{
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS pr_states (
			pr_url TEXT PRIMARY KEY,
			ticket_id TEXT NOT NULL DEFAULT '',
			agent_backend TEXT NOT NULL DEFAULT '',
			last_comment_at_ns INTEGER NOT NULL DEFAULT 0,
			last_review_at_ns INTEGER NOT NULL DEFAULT 0,
			last_ci_sha TEXT NOT NULL DEFAULT '',
			iterations INTEGER NOT NULL DEFAULT 0,
			last_iterated_at_ns INTEGER NOT NULL DEFAULT 0,
			updated_at_ns INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS sweep_states (
			key TEXT PRIMARY KEY,
			last_run_at_ns INTEGER NOT NULL DEFAULT 0,
			updated_at_ns INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS agent_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			recorded_at_ns INTEGER NOT NULL,
			identifier TEXT NOT NULL DEFAULT '',
			ticket_id TEXT NOT NULL DEFAULT '',
			pr_url TEXT NOT NULL DEFAULT '',
			agent_backend TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_usage_recorded_at ON agent_usage(recorded_at_ns)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_usage_ticket ON agent_usage(ticket_id, recorded_at_ns)`,
		`CREATE TABLE IF NOT EXISTS run_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			identifier TEXT NOT NULL DEFAULT '',
			ticket_id TEXT NOT NULL DEFAULT '',
			repo TEXT NOT NULL DEFAULT '',
			pr_url TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			agent_backend TEXT NOT NULL DEFAULT '',
			iterations INTEGER NOT NULL DEFAULT 0,
			started_at_ns INTEGER NOT NULL DEFAULT 0,
			completed_at_ns INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_run_history_started_at ON run_history(started_at_ns)`,
		`CREATE INDEX IF NOT EXISTS idx_run_history_repo_started ON run_history(repo, started_at_ns)`,
		`CREATE TABLE IF NOT EXISTS migrations (
			name TEXT PRIMARY KEY,
			applied_at_ns INTEGER NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init sqlite state: %w", err)
		}
	}
	return nil
}

// Get returns a copy of the record for prURL, or a zero-value PRState if the
// PR isn't tracked yet.
func (s *Store) Get(prURL string) PRState {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, err := s.getLocked(prURL)
	if err != nil {
		slog.Error("state get failed", "pr_url", prURL, "err", err)
		return PRState{}
	}
	return r
}

func (s *Store) getLocked(prURL string) (PRState, error) {
	return getPRState(s.db, prURL)
}

func getPRState(q rowQuerier, prURL string) (PRState, error) {
	var r PRState
	var lastComment, lastReview, lastIterated int64
	err := q.QueryRow(`SELECT ticket_id, agent_backend, last_comment_at_ns, last_review_at_ns,
		last_ci_sha, iterations, last_iterated_at_ns FROM pr_states WHERE pr_url = ?`, prURL).
		Scan(&r.TicketID, &r.AgentBackend, &lastComment, &lastReview, &r.LastCISHA, &r.Iterations, &lastIterated)
	if errors.Is(err, sql.ErrNoRows) {
		return PRState{}, nil
	}
	if err != nil {
		return PRState{}, fmt.Errorf("read pr state: %w", err)
	}
	r.LastCommentAt = timeFromUnixNano(lastComment)
	r.LastReviewAt = timeFromUnixNano(lastReview)
	r.LastIteratedAt = timeFromUnixNano(lastIterated)
	return r, nil
}

// All returns a copy of every tracked PR keyed by URL.
func (s *Store) All() map[string]PRState {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT pr_url, ticket_id, agent_backend, last_comment_at_ns, last_review_at_ns,
		last_ci_sha, iterations, last_iterated_at_ns FROM pr_states ORDER BY pr_url`)
	if err != nil {
		slog.Error("state list failed", "err", err)
		return map[string]PRState{}
	}
	defer rows.Close()

	out := map[string]PRState{}
	for rows.Next() {
		var prURL string
		var r PRState
		var lastComment, lastReview, lastIterated int64
		if err := rows.Scan(&prURL, &r.TicketID, &r.AgentBackend, &lastComment, &lastReview, &r.LastCISHA, &r.Iterations, &lastIterated); err != nil {
			slog.Error("state list scan failed", "err", err)
			return out
		}
		r.LastCommentAt = timeFromUnixNano(lastComment)
		r.LastReviewAt = timeFromUnixNano(lastReview)
		r.LastIteratedAt = timeFromUnixNano(lastIterated)
		out[prURL] = r
	}
	if err := rows.Err(); err != nil {
		slog.Error("state list iteration failed", "err", err)
	}
	return out
}

// Update mutates the record for prURL in place via fn and writes it in one
// transaction. A nil fn is a no-op. fn is called while the store is locked; do
// not call back into the Store from inside fn.
func (s *Store) Update(prURL string, fn func(*PRState)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin pr state update: %w", err)
	}
	defer tx.Rollback()

	r, err := getPRState(tx, prURL)
	if err != nil {
		return fmt.Errorf("load pr state for update: %w", err)
	}
	fn(&r)
	_, err = tx.Exec(`INSERT INTO pr_states (
			pr_url, ticket_id, agent_backend, last_comment_at_ns, last_review_at_ns,
			last_ci_sha, iterations, last_iterated_at_ns, updated_at_ns
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pr_url) DO UPDATE SET
			ticket_id = excluded.ticket_id,
			agent_backend = excluded.agent_backend,
			last_comment_at_ns = excluded.last_comment_at_ns,
			last_review_at_ns = excluded.last_review_at_ns,
			last_ci_sha = excluded.last_ci_sha,
			iterations = excluded.iterations,
			last_iterated_at_ns = excluded.last_iterated_at_ns,
			updated_at_ns = excluded.updated_at_ns`,
		prURL, r.TicketID, r.AgentBackend, unixNano(r.LastCommentAt), unixNano(r.LastReviewAt),
		r.LastCISHA, r.Iterations, unixNano(r.LastIteratedAt), time.Now().UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("write pr state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pr state update: %w", err)
	}
	return nil
}

// SweepKey builds the state key for a sweep task on a specific repo.
// Format: "<repo-slug>/<task-name>".
func SweepKey(repoSlug, taskName string) string {
	return repoSlug + "/" + taskName
}

// GetSweep returns a copy of the sweep state for the given key, or a
// zero-value SweepState if the task hasn't run yet.
func (s *Store) GetSweep(key string) SweepState {
	s.mu.Lock()
	defer s.mu.Unlock()

	ss, err := s.getSweepLocked(key)
	if err != nil {
		slog.Error("state sweep get failed", "key", key, "err", err)
		return SweepState{}
	}
	return ss
}

func (s *Store) getSweepLocked(key string) (SweepState, error) {
	return getSweepState(s.db, key)
}

func getSweepState(q rowQuerier, key string) (SweepState, error) {
	var lastRun int64
	err := q.QueryRow(`SELECT last_run_at_ns FROM sweep_states WHERE key = ?`, key).Scan(&lastRun)
	if errors.Is(err, sql.ErrNoRows) {
		return SweepState{}, nil
	}
	if err != nil {
		return SweepState{}, fmt.Errorf("read sweep state: %w", err)
	}
	return SweepState{LastRunAt: timeFromUnixNano(lastRun)}, nil
}

// UpdateSweep mutates the sweep state for the given key and writes it.
func (s *Store) UpdateSweep(key string, fn func(*SweepState)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin sweep state update: %w", err)
	}
	defer tx.Rollback()

	ss, err := getSweepState(tx, key)
	if err != nil {
		return fmt.Errorf("load sweep state for update: %w", err)
	}
	fn(&ss)
	_, err = tx.Exec(`INSERT INTO sweep_states (key, last_run_at_ns, updated_at_ns)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			last_run_at_ns = excluded.last_run_at_ns,
			updated_at_ns = excluded.updated_at_ns`,
		key, unixNano(ss.LastRunAt), time.Now().UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("write sweep state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sweep state update: %w", err)
	}
	return nil
}

func (s *Store) migrateJSON(path string) error {
	if path == s.path {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat legacy state %s: %w", path, err)
	}

	var already bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM migrations WHERE name = 'legacy_json_v1')`).Scan(&already)
	if err != nil {
		return fmt.Errorf("check legacy migration: %w", err)
	}
	if already {
		return nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read legacy state %s: %w", path, err)
	}
	var data fileFormat
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("parse legacy state %s: %w", path, err)
	}
	if data.PRs == nil {
		data.PRs = map[string]*PRState{}
	}
	if data.Sweeps == nil {
		data.Sweeps = map[string]*SweepState{}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin legacy state migration: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixNano()
	for prURL, r := range data.PRs {
		if r == nil {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO pr_states (
				pr_url, ticket_id, agent_backend, last_comment_at_ns, last_review_at_ns,
				last_ci_sha, iterations, last_iterated_at_ns, updated_at_ns
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			prURL, r.TicketID, r.AgentBackend, unixNano(r.LastCommentAt), unixNano(r.LastReviewAt),
			r.LastCISHA, r.Iterations, unixNano(r.LastIteratedAt), now); err != nil {
			return fmt.Errorf("migrate legacy pr state: %w", err)
		}
	}
	for key, ss := range data.Sweeps {
		if ss == nil {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO sweep_states (key, last_run_at_ns, updated_at_ns)
			VALUES (?, ?, ?)`, key, unixNano(ss.LastRunAt), now); err != nil {
			return fmt.Errorf("migrate legacy sweep state: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO migrations (name, applied_at_ns) VALUES ('legacy_json_v1', ?)`, now); err != nil {
		return fmt.Errorf("mark legacy state migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy state migration: %w", err)
	}
	slog.Info("migrated legacy JSON state to SQLite", "json", path, "db", s.path, "prs", len(data.PRs), "sweeps", len(data.Sweeps))
	return nil
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixNano()
}

func timeFromUnixNano(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}
