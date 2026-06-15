// Package state persists Noctra's view of the PRs it has created and how
// far it has caught up on each — last-seen comment / review cursors and the
// per-PR iteration counter. The watcher reads this on startup so a restart
// doesn't re-react to comments that pre-date the cursor.
//
// Backed by a SQLite database at the path passed to Open. Concurrent-safe:
// the store guards all access with a mutex (matching the previous JSON
// implementation's concurrency model). On first Open, it auto-migrates any
// existing JSON state file (the legacy format) into the database.
package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver — no CGO required.
)

// PRState is the per-PR record stored in the database.
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

// Store is a thread-safe, SQLite-backed PR state.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

// schema is the DDL applied on every Open. Each statement is idempotent
// (IF NOT EXISTS) so it doubles as a forward-migration mechanism — new tables
// or indices added here are created automatically on the next startup.
const schema = `
CREATE TABLE IF NOT EXISTS pr_state (
	pr_url         TEXT PRIMARY KEY,
	ticket_id      TEXT NOT NULL DEFAULT '',
	agent_backend  TEXT NOT NULL DEFAULT '',
	last_comment_at  TEXT NOT NULL DEFAULT '',
	last_review_at   TEXT NOT NULL DEFAULT '',
	last_ci_sha      TEXT NOT NULL DEFAULT '',
	iterations       INTEGER NOT NULL DEFAULT 0,
	last_iterated_at TEXT NOT NULL DEFAULT ''
);

-- ENG-217: token/cost usage as a time series.
CREATE TABLE IF NOT EXISTS cost_usage (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	ticket_id     TEXT NOT NULL DEFAULT '',
	agent_backend TEXT NOT NULL DEFAULT '',
	tokens        INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	recorded_at   TEXT NOT NULL DEFAULT ''
);

-- ENG-218: run history for dashboard analytics.
CREATE TABLE IF NOT EXISTS run_history (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	ticket_id     TEXT NOT NULL DEFAULT '',
	pr_url        TEXT NOT NULL DEFAULT '',
	repo          TEXT NOT NULL DEFAULT '',
	agent_backend TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT '',
	iterations    INTEGER NOT NULL DEFAULT 0,
	started_at    TEXT NOT NULL DEFAULT '',
	completed_at  TEXT NOT NULL DEFAULT '',
	error_message TEXT NOT NULL DEFAULT ''
);
`

// Open opens (or creates) the SQLite database at path and applies the schema.
// A missing file is not an error — SQLite creates it on first access. If a
// legacy JSON state file exists at jsonPath (non-empty), its contents are
// migrated into the database idempotently.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir for state db: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open state db %s: %w", path, err)
	}

	// Apply schema — idempotent, safe to run on every startup.
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema to %s: %w", path, err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection. Safe to call multiple times.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Get returns a copy of the record for prURL, or a zero-value PRState if the
// PR isn't tracked yet.
func (s *Store) Get(prURL string) PRState {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		r                          PRState
		lastCommentAt, lastReviewAt string
		lastIteratedAt             string
	)
	err := s.db.QueryRow(
		`SELECT ticket_id, agent_backend, last_comment_at, last_review_at,
		        last_ci_sha, iterations, last_iterated_at
		 FROM pr_state WHERE pr_url = ?`, prURL,
	).Scan(&r.TicketID, &r.AgentBackend, &lastCommentAt, &lastReviewAt,
		&r.LastCISHA, &r.Iterations, &lastIteratedAt)

	if err != nil {
		return PRState{}
	}

	r.LastCommentAt = parseTime(lastCommentAt)
	r.LastReviewAt = parseTime(lastReviewAt)
	r.LastIteratedAt = parseTime(lastIteratedAt)
	return r
}

// All returns a copy of every tracked PR keyed by URL.
func (s *Store) All() map[string]PRState {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT pr_url, ticket_id, agent_backend, last_comment_at, last_review_at,
		        last_ci_sha, iterations, last_iterated_at
		 FROM pr_state`)
	if err != nil {
		return map[string]PRState{}
	}
	defer rows.Close()

	out := map[string]PRState{}
	for rows.Next() {
		var (
			prURL                       string
			r                           PRState
			lastCommentAt, lastReviewAt string
			lastIteratedAt              string
		)
		if err := rows.Scan(&prURL, &r.TicketID, &r.AgentBackend,
			&lastCommentAt, &lastReviewAt,
			&r.LastCISHA, &r.Iterations, &lastIteratedAt); err != nil {
			continue
		}
		r.LastCommentAt = parseTime(lastCommentAt)
		r.LastReviewAt = parseTime(lastReviewAt)
		r.LastIteratedAt = parseTime(lastIteratedAt)
		out[prURL] = r
	}
	return out
}

// Update mutates the record for prURL in place via fn and writes the result.
// A nil fn is a no-op. fn is called while the store is locked; do not call
// back into the Store from inside fn.
func (s *Store) Update(prURL string, fn func(*PRState)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Read existing row (if any).
	var (
		r                          PRState
		lastCommentAt, lastReviewAt string
		lastIteratedAt             string
	)
	err := s.db.QueryRow(
		`SELECT ticket_id, agent_backend, last_comment_at, last_review_at,
		        last_ci_sha, iterations, last_iterated_at
		 FROM pr_state WHERE pr_url = ?`, prURL,
	).Scan(&r.TicketID, &r.AgentBackend, &lastCommentAt, &lastReviewAt,
		&r.LastCISHA, &r.Iterations, &lastIteratedAt)

	if err == nil {
		r.LastCommentAt = parseTime(lastCommentAt)
		r.LastReviewAt = parseTime(lastReviewAt)
		r.LastIteratedAt = parseTime(lastIteratedAt)
	}
	// sql.ErrNoRows is fine — r stays zero-valued for a new PR.

	fn(&r)

	_, err = s.db.Exec(
		`INSERT INTO pr_state (pr_url, ticket_id, agent_backend, last_comment_at, last_review_at, last_ci_sha, iterations, last_iterated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(pr_url) DO UPDATE SET
		   ticket_id = excluded.ticket_id,
		   agent_backend = excluded.agent_backend,
		   last_comment_at = excluded.last_comment_at,
		   last_review_at = excluded.last_review_at,
		   last_ci_sha = excluded.last_ci_sha,
		   iterations = excluded.iterations,
		   last_iterated_at = excluded.last_iterated_at`,
		prURL, r.TicketID, r.AgentBackend,
		formatTime(r.LastCommentAt), formatTime(r.LastReviewAt),
		r.LastCISHA, r.Iterations, formatTime(r.LastIteratedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert pr_state %s: %w", prURL, err)
	}
	return nil
}

// MigrateJSON reads the legacy JSON state file at jsonPath and inserts its
// contents into the database. It is idempotent: rows that already exist in
// the database are left untouched (INSERT OR IGNORE). After a successful
// migration the JSON file is renamed to jsonPath+".migrated" so it isn't
// re-read on subsequent startups. A missing JSON file is a no-op.
func (s *Store) MigrateJSON(jsonPath string) error {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate
		}
		return fmt.Errorf("read legacy state %s: %w", jsonPath, err)
	}

	var legacy struct {
		PRs map[string]*PRState `json:"prs"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return fmt.Errorf("parse legacy state %s: %w", jsonPath, err)
	}
	if legacy.PRs == nil || len(legacy.PRs) == 0 {
		// Empty file — rename and move on.
		return renameMigrated(jsonPath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO pr_state
		 (pr_url, ticket_id, agent_backend, last_comment_at, last_review_at, last_ci_sha, iterations, last_iterated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare migration stmt: %w", err)
	}
	defer stmt.Close()

	migrated := 0
	for prURL, r := range legacy.PRs {
		if r == nil {
			continue
		}
		if _, err := stmt.Exec(
			prURL, r.TicketID, r.AgentBackend,
			formatTime(r.LastCommentAt), formatTime(r.LastReviewAt),
			r.LastCISHA, r.Iterations, formatTime(r.LastIteratedAt),
		); err != nil {
			return fmt.Errorf("migrate PR %s: %w", prURL, err)
		}
		migrated++
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	slog.Info("migrated legacy JSON state to SQLite",
		"source", jsonPath, "prs", migrated)

	return renameMigrated(jsonPath)
}

// renameMigrated renames a migrated JSON file so it's not re-read. Best-effort.
func renameMigrated(jsonPath string) error {
	dest := jsonPath + ".migrated"
	if _, err := os.Stat(dest); err == nil {
		// Already renamed — perhaps from a prior run.
		return nil
	}
	if err := os.Rename(jsonPath, dest); err != nil {
		slog.Warn("could not rename migrated state file",
			"from", jsonPath, "to", dest, "err", err)
		// Non-fatal: the INSERT OR IGNORE makes re-migration idempotent.
	}
	return nil
}

// formatTime serialises a time.Time to RFC 3339 for storage. The zero time
// is stored as an empty string so the database stays human-readable.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// parseTime is the inverse of formatTime.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
