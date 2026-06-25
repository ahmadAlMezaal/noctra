// Package state persists Noctra's view of PRs it has created and how far it
// has caught up on each: last-seen comment / review cursors, CI cursor, and
// the per-PR iteration counter. The watcher reads this on startup so a restart
// does not re-react to comments that pre-date the cursor.
//
// It also tracks sweep task cooldowns (ENG-222): per-repo, per-task last-run
// timestamps so the same maintenance task is not re-run before its cooldown
// expires.
//
// The active store is SQLite. A legacy JSON state file can be migrated once at
// startup via OpenMigrating.
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	// (e.g. "claude", "codex", "copilot", "antigravity"). Persisted so the auto-iterate
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

	// LastCISHA is the head commit SHA whose failing CI Noctra has already
	// re-engaged on. CI is keyed by SHA (not timestamp) so a failure is acted on
	// once per commit; pushing a fix changes the SHA, making a fresh failure
	// eligible again, bounded by Iterations.
	LastCISHA string `json:"last_ci_sha,omitempty"`

	// Iterations counts how many times Noctra has re-engaged on this PR. Capped
	// by config.MaxPRIterations.
	Iterations int `json:"iterations,omitempty"`

	// LastIteratedAt is the timestamp of the most recent re-engage. Mostly for
	// telemetry; also useful for spotting stuck iteration loops.
	LastIteratedAt time.Time `json:"last_iterated_at,omitempty"`

	// LastPushedSHA is the git commit SHA that Noctra last pushed to this PR's branch.
	LastPushedSHA string `json:"last_pushed_sha,omitempty"`

	// MergedProcessed reports whether human post-merge edits for this PR have been processed.
	MergedProcessed bool `json:"merged_processed,omitempty"`

	// LastReasoning is the agent's summary from the most recent re-engagement,
	// reused in the next fix prompt and the GitHub thread reply.
	LastReasoning string `json:"last_reasoning,omitempty"`
}

// SweepState is the per-task-per-repo record for autonomous maintenance
// sweeps (ENG-222). The key is "repo-slug/task-name".
type SweepState struct {
	// LastRunAt is when this task last ran on this repo.
	LastRunAt time.Time `json:"last_run_at,omitempty"`
}

// PlanState tracks a ticket that has been planned but not yet approved
// (ENG-221). Keyed by the source ticket identifier (e.g. "ENG-42").
type PlanState struct {
	// Source is the ticket source name. Empty means "linear" for records
	// written before multi-source support.
	Source string `json:"source,omitempty"`
	// IssueID is the source issue ID (needed for approval-comment fetching).
	IssueID string `json:"issue_id"`
	// Plan is the implementation plan the agent produced.
	Plan string `json:"plan"`
	// PlannedAt is when the plan was posted.
	PlannedAt time.Time `json:"planned_at"`
}

// RunHistory records the outcome of a single Noctra run (ticket, sweep, or
// iterate). Written at each lifecycle's terminal point so the dashboard's
// "what happened overnight" panel survives restarts.
type RunHistory struct {
	Identifier   string
	TicketID     string
	PRURL        string
	Repo         string
	AgentBackend string
	RunType      string // "ticket" | "sweep" | "iterate"
	StartedAt    time.Time
	FinishedAt   time.Time
	Status       string // "pr_opened" | "blocked" | "failed" | "no_change"
	Iterations   int
}

// UsageEvent records token consumption from a single agent invocation.
// Written beside every budget.Record call so cost data survives restarts.
type UsageEvent struct {
	OccurredAt   time.Time
	Source       string // "ticket" | "iterate" | "sweep" | "plan"
	TicketID     string
	PRURL        string
	AgentBackend string
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	CostUSD      float64
}

// OAuthState persists the rotating Linear actor=app OAuth credentials (ENG-236)
// so a restart does not lose a refresh-token rotation.
type OAuthState struct {
	AccessToken  string    `json:"access_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
}

type fileFormat struct {
	PRs    map[string]*PRState    `json:"prs"`
	Sweeps map[string]*SweepState `json:"sweeps,omitempty"`
	OAuth  *OAuthState            `json:"oauth,omitempty"`
}

// Store is a thread-safe, SQLite-backed PR, sweep, and OAuth state store.
type Store struct {
	mu   sync.Mutex
	path string
	db   *sql.DB
}

// Open opens the SQLite state database at path. A missing database is created
// with the current schema.
func Open(path string) (*Store, error) {
	s, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// OpenMigrating opens the SQLite state database at dbPath and, when the DB did
// not already exist, migrates records from legacyJSONPath if that file exists.
// Existing DBs are never clobbered.
func OpenMigrating(dbPath, legacyJSONPath string) (*Store, error) {
	dbExisted := pathExists(dbPath)
	s, err := openSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	if dbExisted || legacyJSONPath == "" || !pathExists(legacyJSONPath) {
		return s, nil
	}
	if err := s.migrateLegacyJSON(legacyJSONPath); err != nil {
		_ = s.Close()
		removeNewStateDB(dbPath)
		return nil, err
	}
	slog.Info("migrated legacy state file to sqlite", "from", legacyJSONPath, "to", dbPath)
	return s, nil
}

func openSQLite(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state db dir: %w", err)
	}
	db, err := sql.Open("sqlite", stateDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open state db %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying SQLite connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pr_states (
			pr_url TEXT PRIMARY KEY,
			ticket_id TEXT NOT NULL DEFAULT '',
			agent_backend TEXT NOT NULL DEFAULT '',
			last_comment_at TEXT,
			last_review_at TEXT,
			last_ci_sha TEXT NOT NULL DEFAULT '',
			iterations INTEGER NOT NULL DEFAULT 0,
			last_iterated_at TEXT,
			last_pushed_sha TEXT NOT NULL DEFAULT '',
			merged_processed INTEGER NOT NULL DEFAULT 0,
			last_reasoning TEXT NOT NULL DEFAULT ''
		)`,
		`ALTER TABLE pr_states ADD COLUMN last_pushed_sha TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pr_states ADD COLUMN merged_processed INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pr_states ADD COLUMN last_reasoning TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS repo_lessons (
			repo TEXT PRIMARY KEY,
			lessons TEXT NOT NULL DEFAULT '',
			updated_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS sweep_states (
			key TEXT PRIMARY KEY,
			last_run_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS plan_states (
			identifier TEXT PRIMARY KEY,
			source TEXT NOT NULL DEFAULT 'linear',
			issue_id TEXT NOT NULL DEFAULT '',
			plan TEXT NOT NULL DEFAULT '',
			planned_at TEXT
		)`,
		`ALTER TABLE plan_states ADD COLUMN source TEXT NOT NULL DEFAULT 'linear'`,
		`CREATE TABLE IF NOT EXISTS oauth_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			access_token TEXT NOT NULL DEFAULT '',
			expires_at TEXT,
			refresh_token TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS usage_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			occurred_at TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			ticket_id TEXT NOT NULL DEFAULT '',
			pr_url TEXT NOT NULL DEFAULT '',
			agent_backend TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS usage_events_occurred_at_idx ON usage_events (occurred_at)`,
		`CREATE INDEX IF NOT EXISTS usage_events_ticket_id_idx ON usage_events (ticket_id)`,
		`CREATE TABLE IF NOT EXISTS run_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			identifier TEXT NOT NULL DEFAULT '',
			ticket_id TEXT NOT NULL DEFAULT '',
			pr_url TEXT NOT NULL DEFAULT '',
			repo TEXT NOT NULL DEFAULT '',
			agent_backend TEXT NOT NULL DEFAULT '',
			run_type TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL,
			finished_at TEXT,
			status TEXT NOT NULL DEFAULT '',
			iterations INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS run_history_started_at_idx ON run_history (started_at)`,
		`CREATE INDEX IF NOT EXISTS run_history_ticket_id_idx ON run_history (ticket_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("init state schema: %w", err)
		}
	}
	return nil
}

// Get returns a copy of the record for prURL, or a zero-value PRState if the
// PR is not tracked yet.
func (s *Store) Get(prURL string) PRState {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.getPRLocked(prURL)
	if err != nil {
		slog.Warn("state get failed", "pr_url", prURL, "err", err)
		return PRState{}
	}
	return r
}

func (s *Store) getPRLocked(prURL string) (PRState, error) {
	var r PRState
	var lastComment, lastReview, lastIterated sql.NullString
	err := s.db.QueryRow(`SELECT ticket_id, agent_backend, last_comment_at, last_review_at, last_ci_sha, iterations, last_iterated_at, last_pushed_sha, merged_processed, last_reasoning
		FROM pr_states WHERE pr_url = ?`, prURL).Scan(
		&r.TicketID,
		&r.AgentBackend,
		&lastComment,
		&lastReview,
		&r.LastCISHA,
		&r.Iterations,
		&lastIterated,
		&r.LastPushedSHA,
		&r.MergedProcessed,
		&r.LastReasoning,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PRState{}, nil
	}
	if err != nil {
		return PRState{}, fmt.Errorf("read pr state: %w", err)
	}
	var convErr error
	if r.LastCommentAt, convErr = parseNullTime(lastComment); convErr != nil {
		return PRState{}, fmt.Errorf("parse last_comment_at: %w", convErr)
	}
	if r.LastReviewAt, convErr = parseNullTime(lastReview); convErr != nil {
		return PRState{}, fmt.Errorf("parse last_review_at: %w", convErr)
	}
	if r.LastIteratedAt, convErr = parseNullTime(lastIterated); convErr != nil {
		return PRState{}, fmt.Errorf("parse last_iterated_at: %w", convErr)
	}
	return r, nil
}

// All returns a copy of every tracked PR keyed by URL.
func (s *Store) All() map[string]PRState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.allLocked()
	if err != nil {
		slog.Warn("state all failed", "err", err)
		return map[string]PRState{}
	}
	return out
}

func (s *Store) allLocked() (map[string]PRState, error) {
	rows, err := s.db.Query(`SELECT pr_url, ticket_id, agent_backend, last_comment_at, last_review_at, last_ci_sha, iterations, last_iterated_at, last_pushed_sha, merged_processed, last_reasoning
		FROM pr_states ORDER BY pr_url`)
	if err != nil {
		return nil, fmt.Errorf("list pr states: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("close pr state rows failed", "err", err)
		}
	}()

	out := map[string]PRState{}
	for rows.Next() {
		var prURL string
		var r PRState
		var lastComment, lastReview, lastIterated sql.NullString
		if err := rows.Scan(
			&prURL,
			&r.TicketID,
			&r.AgentBackend,
			&lastComment,
			&lastReview,
			&r.LastCISHA,
			&r.Iterations,
			&lastIterated,
			&r.LastPushedSHA,
			&r.MergedProcessed,
			&r.LastReasoning,
		); err != nil {
			return nil, fmt.Errorf("scan pr state: %w", err)
		}
		var convErr error
		if r.LastCommentAt, convErr = parseNullTime(lastComment); convErr != nil {
			return nil, fmt.Errorf("parse last_comment_at: %w", convErr)
		}
		if r.LastReviewAt, convErr = parseNullTime(lastReview); convErr != nil {
			return nil, fmt.Errorf("parse last_review_at: %w", convErr)
		}
		if r.LastIteratedAt, convErr = parseNullTime(lastIterated); convErr != nil {
			return nil, fmt.Errorf("parse last_iterated_at: %w", convErr)
		}
		out[prURL] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pr states: %w", err)
	}
	return out, nil
}

// Update mutates the record for prURL in place via fn and writes it. A nil fn
// is a no-op. fn is called while the store is locked; do not call back into the
// Store from inside fn.
func (s *Store) Update(prURL string, fn func(*PRState)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.getPRLocked(prURL)
	if err != nil {
		return err
	}
	fn(&r)
	_, err = s.db.Exec(`INSERT INTO pr_states (
			pr_url, ticket_id, agent_backend, last_comment_at, last_review_at, last_ci_sha, iterations, last_iterated_at, last_pushed_sha, merged_processed, last_reasoning
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pr_url) DO UPDATE SET
			ticket_id = excluded.ticket_id,
			agent_backend = excluded.agent_backend,
			last_comment_at = excluded.last_comment_at,
			last_review_at = excluded.last_review_at,
			last_ci_sha = excluded.last_ci_sha,
			iterations = excluded.iterations,
			last_iterated_at = excluded.last_iterated_at,
			last_pushed_sha = excluded.last_pushed_sha,
			merged_processed = excluded.merged_processed,
			last_reasoning = excluded.last_reasoning`,
		prURL,
		r.TicketID,
		r.AgentBackend,
		formatTime(r.LastCommentAt),
		formatTime(r.LastReviewAt),
		r.LastCISHA,
		r.Iterations,
		formatTime(r.LastIteratedAt),
		r.LastPushedSHA,
		r.MergedProcessed,
		r.LastReasoning,
	)
	if err != nil {
		return fmt.Errorf("write pr state: %w", err)
	}
	return nil
}

// LoadOAuth returns the persisted access token, expiry, and refresh token (zero
// values if none). Satisfies linear.TokenStore.
func (s *Store) LoadOAuth() (access string, expiresAt time.Time, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expires sql.NullString
	err := s.db.QueryRow(`SELECT access_token, expires_at, refresh_token FROM oauth_state WHERE id = 1`).Scan(&access, &expires, &refresh)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ""
	}
	if err != nil {
		slog.Warn("state oauth load failed", "err", err)
		return "", time.Time{}, ""
	}
	expiresAt, err = parseNullTime(expires)
	if err != nil {
		slog.Warn("state oauth expiry parse failed", "err", err)
		return "", time.Time{}, ""
	}
	return access, expiresAt, refresh
}

// SaveOAuth persists the credentials atomically. Satisfies linear.TokenStore.
func (s *Store) SaveOAuth(access string, expiresAt time.Time, refresh string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO oauth_state (id, access_token, expires_at, refresh_token)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			access_token = excluded.access_token,
			expires_at = excluded.expires_at,
			refresh_token = excluded.refresh_token`,
		access,
		formatTime(expiresAt),
		refresh,
	)
	if err != nil {
		return fmt.Errorf("write oauth state: %w", err)
	}
	return nil
}

// SweepKey builds the state key for a sweep task on a specific repo.
// Format: "<repo-slug>/<task-name>".
func SweepKey(repoSlug, taskName string) string {
	return repoSlug + "/" + taskName
}

// GetSweep returns a copy of the sweep state for the given key, or a
// zero-value SweepState if the task has not run yet.
func (s *Store) GetSweep(key string) SweepState {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.getSweepLocked(key)
	if err != nil {
		slog.Warn("state get sweep failed", "key", key, "err", err)
		return SweepState{}
	}
	return r
}

func (s *Store) getSweepLocked(key string) (SweepState, error) {
	var lastRun sql.NullString
	err := s.db.QueryRow(`SELECT last_run_at FROM sweep_states WHERE key = ?`, key).Scan(&lastRun)
	if errors.Is(err, sql.ErrNoRows) {
		return SweepState{}, nil
	}
	if err != nil {
		return SweepState{}, fmt.Errorf("read sweep state: %w", err)
	}
	lastRunAt, err := parseNullTime(lastRun)
	if err != nil {
		return SweepState{}, fmt.Errorf("parse last_run_at: %w", err)
	}
	return SweepState{LastRunAt: lastRunAt}, nil
}

// UpdateSweep mutates the sweep state for the given key and writes it.
func (s *Store) UpdateSweep(key string, fn func(*SweepState)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.getSweepLocked(key)
	if err != nil {
		return err
	}
	fn(&r)
	_, err = s.db.Exec(`INSERT INTO sweep_states (key, last_run_at)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET last_run_at = excluded.last_run_at`,
		key,
		formatTime(r.LastRunAt),
	)
	if err != nil {
		return fmt.Errorf("write sweep state: %w", err)
	}
	return nil
}

// GetPlan returns the plan state for a ticket, or a zero-value PlanState if
// no plan has been recorded. (ENG-221)
func (s *Store) GetPlan(identifier string) PlanState {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r PlanState
	var plannedAt sql.NullString
	err := s.db.QueryRow(`SELECT source, issue_id, plan, planned_at FROM plan_states WHERE identifier = ?`, identifier).
		Scan(&r.Source, &r.IssueID, &r.Plan, &plannedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanState{}
	}
	if err != nil {
		slog.Warn("state get plan failed", "identifier", identifier, "err", err)
		return PlanState{}
	}
	if t, convErr := parseNullTime(plannedAt); convErr == nil {
		r.PlannedAt = t
	}
	return r
}

// SavePlan persists a plan awaiting human approval. (ENG-221)
func (s *Store) SavePlan(identifier string, ps PlanState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ps.Source == "" {
		ps.Source = "linear"
	}
	_, err := s.db.Exec(`INSERT INTO plan_states (identifier, source, issue_id, plan, planned_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(identifier) DO UPDATE SET
			source = excluded.source,
			issue_id = excluded.issue_id,
			plan = excluded.plan,
			planned_at = excluded.planned_at`,
		identifier,
		ps.Source,
		ps.IssueID,
		ps.Plan,
		formatTime(ps.PlannedAt),
	)
	if err != nil {
		return fmt.Errorf("write plan state: %w", err)
	}
	return nil
}

// DeletePlan removes a plan record after approval or rejection. (ENG-221)
func (s *Store) DeletePlan(identifier string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM plan_states WHERE identifier = ?`, identifier)
	if err != nil {
		return fmt.Errorf("delete plan state: %w", err)
	}
	return nil
}

// AllPlans returns every pending plan keyed by identifier. (ENG-221)
func (s *Store) AllPlans() map[string]PlanState {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT identifier, source, issue_id, plan, planned_at FROM plan_states`)
	if err != nil {
		slog.Warn("state all plans failed", "err", err)
		return nil
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("close plan state rows failed", "err", err)
		}
	}()
	out := map[string]PlanState{}
	for rows.Next() {
		var id string
		var r PlanState
		var plannedAt sql.NullString
		if err := rows.Scan(&id, &r.Source, &r.IssueID, &r.Plan, &plannedAt); err != nil {
			slog.Warn("scan plan state failed", "err", err)
			return nil
		}
		if t, convErr := parseNullTime(plannedAt); convErr == nil {
			r.PlannedAt = t
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		slog.Warn("iterate plan states failed", "err", err)
		return nil
	}
	return out
}

// InsertRunHistory persists a run-history record. Best-effort: a failed
// insert is logged and never blocks the run.
func (s *Store) InsertRunHistory(rec RunHistory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO run_history (
			identifier, ticket_id, pr_url, repo, agent_backend,
			run_type, started_at, finished_at, status, iterations
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Identifier,
		rec.TicketID,
		rec.PRURL,
		rec.Repo,
		rec.AgentBackend,
		rec.RunType,
		formatTime(rec.StartedAt),
		formatTime(rec.FinishedAt),
		rec.Status,
		rec.Iterations,
	)
	if err != nil {
		return fmt.Errorf("insert run history: %w", err)
	}
	return nil
}

// ListRunHistory returns the most recent n run-history records, newest first.
func (s *Store) ListRunHistory(limit int) ([]RunHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT identifier, ticket_id, pr_url, repo, agent_backend,
		run_type, started_at, finished_at, status, iterations
		FROM run_history ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list run history: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Warn("close run_history rows failed", "err", err)
		}
	}()
	var out []RunHistory
	for rows.Next() {
		var r RunHistory
		var startedAt, finishedAt sql.NullString
		if err := rows.Scan(
			&r.Identifier, &r.TicketID, &r.PRURL, &r.Repo, &r.AgentBackend,
			&r.RunType, &startedAt, &finishedAt, &r.Status, &r.Iterations,
		); err != nil {
			return nil, fmt.Errorf("scan run history: %w", err)
		}
		if t, convErr := parseNullTime(startedAt); convErr == nil {
			r.StartedAt = t
		}
		if t, convErr := parseNullTime(finishedAt); convErr == nil {
			r.FinishedAt = t
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run history: %w", err)
	}
	return out, nil
}

// RecordUsage persists a usage-event row. Best-effort: a failed insert is
// logged and never blocks the run.
func (s *Store) RecordUsage(ev UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO usage_events (
			occurred_at, source, ticket_id, pr_url, agent_backend,
			input_tokens, output_tokens, total_tokens, cost_usd
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(ev.OccurredAt),
		ev.Source,
		ev.TicketID,
		ev.PRURL,
		ev.AgentBackend,
		ev.InputTokens,
		ev.OutputTokens,
		ev.TotalTokens,
		ev.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("record usage event: %w", err)
	}
	return nil
}

func (s *Store) migrateLegacyJSON(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read legacy state %s: %w", path, err)
	}
	var data fileFormat
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("parse legacy state %s: %w", path, err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin legacy state migration: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			slog.Warn("rollback legacy state migration failed", "err", err)
		}
	}()

	for prURL, r := range data.PRs {
		if r == nil {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO pr_states (
				pr_url, ticket_id, agent_backend, last_comment_at, last_review_at, last_ci_sha, iterations, last_iterated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(pr_url) DO NOTHING`,
			prURL,
			r.TicketID,
			r.AgentBackend,
			formatTime(r.LastCommentAt),
			formatTime(r.LastReviewAt),
			r.LastCISHA,
			r.Iterations,
			formatTime(r.LastIteratedAt),
		); err != nil {
			return fmt.Errorf("migrate pr state %s: %w", prURL, err)
		}
	}

	for key, r := range data.Sweeps {
		if r == nil {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO sweep_states (key, last_run_at)
			VALUES (?, ?)
			ON CONFLICT(key) DO NOTHING`,
			key,
			formatTime(r.LastRunAt),
		); err != nil {
			return fmt.Errorf("migrate sweep state %s: %w", key, err)
		}
	}

	if data.OAuth != nil {
		if _, err := tx.Exec(`INSERT INTO oauth_state (id, access_token, expires_at, refresh_token)
			VALUES (1, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING`,
			data.OAuth.AccessToken,
			formatTime(data.OAuth.ExpiresAt),
			data.OAuth.RefreshToken,
		); err != nil {
			return fmt.Errorf("migrate oauth state: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy state migration: %w", err)
	}
	return nil
}

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func stateDSN(path string) string {
	values := url.Values{}
	values.Add("_pragma", "busy_timeout=5000")
	values.Add("_pragma", "journal_mode(WAL)")

	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + values.Encode()
}

func removeNewStateDB(path string) {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("remove failed state db after migration error", "path", candidate, "err", err)
		}
	}
}

// GetLessons returns the lessons for a repository, or empty string if none exist.
func (s *Store) GetLessons(repo string) (string, error) {
	if s == nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var lessons string
	err := s.db.QueryRow(`SELECT lessons FROM repo_lessons WHERE repo = ?`, repo).Scan(&lessons)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read repo lessons: %w", err)
	}
	return lessons, nil
}

// SaveLessons persists lessons for a repository.
func (s *Store) SaveLessons(repo string, lessons string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO repo_lessons (repo, lessons, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(repo) DO UPDATE SET
			lessons = excluded.lessons,
			updated_at = excluded.updated_at`,
		repo,
		lessons,
		formatTime(time.Now()),
	)
	if err != nil {
		return fmt.Errorf("write repo lessons: %w", err)
	}
	return nil
}

func formatTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339Nano), Valid: true}
}

func parseNullTime(v sql.NullString) (time.Time, error) {
	if !v.Valid || v.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}
