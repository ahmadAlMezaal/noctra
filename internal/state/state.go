// Package state persists Noctra's view of the PRs it has created and how
// far it has caught up on each — last-seen comment / review cursors and the
// per-PR iteration counter. The watcher reads this on startup so a restart
// doesn't re-react to comments that pre-date the cursor.
//
// It also tracks sweep task cooldowns (ENG-222): per-repo, per-task last-run
// timestamps so the same maintenance task isn't re-run before its cooldown
// expires.
//
// Backed by a single JSON file at the path passed to Open. Concurrent-safe:
// the store guards its in-memory map with a mutex and writes the file
// atomically (write-temp, rename) on every update.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PRState is the per-PR record stored in the state file.
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

// Store is a thread-safe, file-backed PR and sweep state.
type Store struct {
	mu   sync.Mutex
	path string
	data fileFormat
}

// Open loads the state file at path. A missing file is not an error — the
// returned store starts empty and Save will create the file on first write.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: fileFormat{
		PRs:    map[string]*PRState{},
		Sweeps: map[string]*SweepState{},
	}}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.data.PRs == nil {
		s.data.PRs = map[string]*PRState{}
	}
	if s.data.Sweeps == nil {
		s.data.Sweeps = map[string]*SweepState{}
	}
	return s, nil
}

// Get returns a copy of the record for prURL, or a zero-value PRState if the
// PR isn't tracked yet.
func (s *Store) Get(prURL string) PRState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.data.PRs[prURL]; ok && r != nil {
		return *r
	}
	return PRState{}
}

// All returns a copy of every tracked PR keyed by URL.
func (s *Store) All() map[string]PRState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]PRState, len(s.data.PRs))
	for k, v := range s.data.PRs {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

// Update mutates the record for prURL in place via fn and writes the file.
// A nil fn is a no-op. fn is called while the store is locked; do not call
// back into the Store from inside fn.
func (s *Store) Update(prURL string, fn func(*PRState)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.PRs[prURL]
	if !ok || r == nil {
		r = &PRState{}
		s.data.PRs[prURL] = r
	}
	fn(r)
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return writeAtomic(s.path, data)
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
	if r, ok := s.data.Sweeps[key]; ok && r != nil {
		return *r
	}
	return SweepState{}
}

// UpdateSweep mutates the sweep state for the given key and writes the file.
func (s *Store) UpdateSweep(key string, fn func(*SweepState)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.Sweeps[key]
	if !ok || r == nil {
		r = &SweepState{}
		s.data.Sweeps[key] = r
	}
	fn(r)
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return writeAtomic(s.path, data)
}

// writeAtomic writes to a sibling temp file then renames over the target.
// On POSIX, rename(2) is atomic — a crash mid-write leaves the previous
// state file intact instead of an empty/corrupt one.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // don't leak the temp file on a failed rename
		return err
	}
	return nil
}
