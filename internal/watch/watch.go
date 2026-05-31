// Package watch polls Nightshift-authored PRs for actionable review feedback
// and emits PRChanges describing what's new since the last poll. Combined
// with the state.Store cursor, the watcher only ever surfaces events that
// haven't been processed yet — restarts don't re-react to historical
// comments.
//
// The watcher is intentionally side-effect-free: it doesn't dispatch Claude,
// doesn't touch worktrees, doesn't post to Linear. It just classifies. The
// pipeline layer drives the actual response.
package watch

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/github"
	"github.com/ahmadAlMezaal/nightshift/internal/state"
)

// EventType distinguishes comments from reviews so the prompt builder can
// label them appropriately.
type EventType string

const (
	EventComment EventType = "comment"
	EventReview  EventType = "review"
)

// Event is one new piece of feedback on a PR — either a conversation comment
// or a submitted review.
type Event struct {
	Type        EventType
	Author      github.Actor
	Body        string
	URL         string
	At          time.Time
	ReviewState string // populated when Type == EventReview
	Path        string // file path, populated for inline review comments
	Line        int    // line number, populated for inline review comments
}

// PRChanges packages everything the pipeline needs to act on one PR's worth
// of new feedback. NewestComment / NewestReview are the cursors the caller
// should write back into the state store *after* the iteration succeeds.
type PRChanges struct {
	PR            github.PR
	Details       *github.Details
	Events        []Event   // actionable (action!)
	Skipped       []Event   // seen but filtered (log + ignore)
	NewestComment time.Time // greatest CreatedAt across all comments
	NewestReview  time.Time // greatest SubmittedAt across all reviews
}

// Watcher couples the gh client with the state store and the trusted-reviewer
// allowlist.
type Watcher struct {
	gh       *github.Client
	store    *state.Store
	trusted  map[string]bool
	repoURLs []string
}

// New constructs a Watcher. trusted is the list of GitHub logins/bot names
// whose feedback should be acted on; humans are always trusted, so trusted
// only meaningfully restricts bots.
func New(gh *github.Client, store *state.Store, repoURLs []string, trusted []string) *Watcher {
	// GitHub logins are case-insensitive; normalise to lower so a config
	// entry like "Gemini-Code-Assist" still matches the API's casing.
	t := make(map[string]bool, len(trusted))
	for _, login := range trusted {
		if login = strings.ToLower(strings.TrimSpace(login)); login != "" {
			t[login] = true
		}
	}
	return &Watcher{gh: gh, store: store, trusted: t, repoURLs: repoURLs}
}

// Scan lists all open Nightshift PRs and returns one PRChanges per PR with
// at least one new event (actionable OR skipped). PRs with no changes since
// the last cursor are omitted from the result so callers can short-circuit.
func (w *Watcher) Scan(ctx context.Context) ([]PRChanges, error) {
	prs, err := w.gh.ListNightshiftPRs(ctx, w.repoURLs)
	if err != nil {
		return nil, err
	}

	var out []PRChanges
	for _, pr := range prs {
		details, err := w.gh.GetPR(ctx, pr.URL)
		if err != nil {
			slog.Warn("watch: get PR failed", "url", pr.URL, "err", err)
			continue
		}
		if !details.IsOpen() {
			continue
		}

		cursor := w.store.Get(pr.URL)
		ch := w.diff(pr, details, cursor)
		// cursorMoved covers PRs whose only new events are non-actionable
		// (APPROVED review, empty COMMENTED wrapper): no Events/Skipped, but
		// still returned so the caller persists the advanced cursor.
		cursorMoved := ch.NewestComment.After(cursor.LastCommentAt) ||
			ch.NewestReview.After(cursor.LastReviewAt)
		if len(ch.Events) > 0 || len(ch.Skipped) > 0 || cursorMoved {
			out = append(out, ch)
		}
	}
	return out, nil
}

// diff produces a PRChanges for one PR by comparing comments+reviews against
// the cursor in state. Events past the cursor get classified into actionable
// or skipped; events at-or-before the cursor are silently ignored (already
// handled in a prior poll).
func (w *Watcher) diff(pr github.PR, d *github.Details, cursor state.PRState) PRChanges {
	out := PRChanges{
		PR:            pr,
		Details:       d,
		NewestComment: cursor.LastCommentAt,
		NewestReview:  cursor.LastReviewAt,
	}

	for _, c := range d.Comments {
		if !c.CreatedAt.After(cursor.LastCommentAt) {
			continue
		}
		if c.CreatedAt.After(out.NewestComment) {
			out.NewestComment = c.CreatedAt
		}
		ev := Event{
			Type:   EventComment,
			Author: c.Author,
			Body:   c.Body,
			URL:    c.URL,
			At:     c.CreatedAt,
		}
		if w.actionable(ev) {
			out.Events = append(out.Events, ev)
		} else {
			out.Skipped = append(out.Skipped, ev)
		}
	}

	// Inline review-thread comments share the comment cursor — they're
	// "comments" too, and comment timestamps are globally ordered, so a new
	// inline comment always sorts after anything seen in a prior poll.
	for _, rc := range d.ReviewComments {
		if !rc.CreatedAt.After(cursor.LastCommentAt) {
			continue
		}
		if rc.CreatedAt.After(out.NewestComment) {
			out.NewestComment = rc.CreatedAt
		}
		ev := Event{
			Type:   EventComment,
			Author: rc.Author,
			Body:   rc.Body,
			URL:    rc.URL,
			At:     rc.CreatedAt,
			Path:   rc.Path,
			Line:   rc.Line,
		}
		if w.actionable(ev) {
			out.Events = append(out.Events, ev)
		} else {
			out.Skipped = append(out.Skipped, ev)
		}
	}

	for _, r := range d.Reviews {
		if !r.SubmittedAt.After(cursor.LastReviewAt) {
			continue
		}
		if r.SubmittedAt.After(out.NewestReview) {
			out.NewestReview = r.SubmittedAt
		}

		// Reviews that don't request a response: skip the prompt but still
		// advance the cursor (no need to revisit them).
		if r.State == "APPROVED" || r.State == "DISMISSED" {
			continue
		}
		if r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
			// Empty "just commented" reviews are wrappers around inline
			// comments; nothing actionable at the review level.
			continue
		}

		ev := Event{
			Type:        EventReview,
			Author:      r.Author,
			Body:        r.Body,
			At:          r.SubmittedAt,
			ReviewState: r.State,
		}
		if w.actionable(ev) {
			out.Events = append(out.Events, ev)
		} else {
			out.Skipped = append(out.Skipped, ev)
		}
	}

	return out
}

// actionable decides whether an event should be acted on. Humans are always
// trusted; bots are acted on only if their login appears in the trusted list.
// This is the lesson from PR #50: a bot reviewer was confidently wrong about
// our golangci-lint config, and blindly applying its suggestion would have
// rewritten valid v2 syntax to broken v1.
func (w *Watcher) actionable(ev Event) bool {
	if !ev.Author.IsBot() {
		return true
	}
	return w.trusted[strings.ToLower(ev.Author.Login)]
}
