// Package watch polls Noctra-authored PRs for actionable review feedback
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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/state"
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
	CommentID   string // source comment ID, for reacting; empty for reviews
}

// CIFailure describes a head commit whose CI has completed with at least one
// failing check and which Noctra hasn't re-engaged on yet.
type CIFailure struct {
	SHA          string         // head commit the failures belong to
	FailedChecks []github.Check // only the failing checks
}

// PRChanges packages everything the pipeline needs to act on one PR's worth
// of new feedback. NewestComment / NewestReview are the cursors the caller
// should write back into the state store *after* the iteration succeeds.
type PRChanges struct {
	PR            github.PR
	Details       *github.Details
	Events        []Event    // actionable (action!)
	Skipped       []Event    // seen but filtered (log + ignore)
	NewestComment time.Time  // greatest CreatedAt across all comments
	NewestReview  time.Time  // greatest SubmittedAt across all reviews
	CIFailure     *CIFailure // non-nil when the head commit's CI failed and is unhandled
}

// Watcher couples the gh client with the state store and the trusted-reviewer
// allowlist.
type Watcher struct {
	gh      *github.Client
	store   *state.Store
	trusted map[string]bool
	// reposFn returns the git URLs of the repos to scan for Noctra PRs.
	// It's a function (not a static slice) because directive-only routing
	// clones repos on demand — the set grows as tickets are dispatched, so the
	// watcher re-discovers them on every poll. It takes the scan context so the
	// underlying git reads honour the poll timeout. May be nil (scans nothing).
	reposFn func(context.Context) []string
}

// New constructs a Watcher. trusted is the list of GitHub logins/bot names
// whose feedback should be acted on; humans are always trusted, so trusted
// only meaningfully restricts bots. reposFn is evaluated on each Scan to get
// the current set of repos to poll.
func New(gh *github.Client, store *state.Store, reposFn func(context.Context) []string, trusted []string) *Watcher {
	// GitHub logins are case-insensitive; normalise to lower so a config
	// entry like "Gemini-Code-Assist" still matches the API's casing.
	t := make(map[string]bool, len(trusted))
	for _, login := range trusted {
		if login = strings.ToLower(strings.TrimSpace(login)); login != "" {
			t[login] = true
		}
	}
	return &Watcher{gh: gh, store: store, trusted: t, reposFn: reposFn}
}

// Scan lists all open Noctra PRs and returns one PRChanges per PR with
// at least one new event (actionable OR skipped). PRs with no changes since
// the last cursor are omitted from the result so callers can short-circuit.
func (w *Watcher) Scan(ctx context.Context) ([]PRChanges, error) {
	var repoURLs []string
	if w.reposFn != nil {
		repoURLs = w.reposFn(ctx)
	}
	prs, err := w.gh.ListNoctraPRs(ctx, repoURLs)
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
		if len(ch.Events) > 0 || len(ch.Skipped) > 0 || cursorMoved || ch.CIFailure != nil {
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
			Type:      EventComment,
			Author:    c.Author,
			Body:      c.Body,
			URL:       c.URL,
			At:        c.CreatedAt,
			CommentID: c.ID,
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
			Type:      EventComment,
			Author:    rc.Author,
			Body:      rc.Body,
			URL:       rc.URL,
			At:        rc.CreatedAt,
			Path:      rc.Path,
			Line:      rc.Line,
			CommentID: strconv.FormatInt(rc.ID, 10),
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

	// CI: act at most once per head commit. Keyed by SHA, not timestamp —
	// pushing a fix changes the SHA, so a fresh failure becomes eligible
	// again (bounded by the iteration cap).
	if d.HeadRefOid != "" && d.HeadRefOid != cursor.LastCISHA {
		if failed := failedChecks(d.StatusCheckRollup); len(failed) > 0 {
			out.CIFailure = &CIFailure{SHA: d.HeadRefOid, FailedChecks: failed}
		}
	}

	return out
}

// failedChecks returns any checks that have reached a final failing state.
// It does not wait for unrelated pending checks: a manual approval or stuck
// queue should not indefinitely hide a real failure that is already final.
func failedChecks(checks []github.Check) []github.Check {
	if len(checks) == 0 {
		return nil
	}
	var failed []github.Check
	for _, c := range checks {
		if c.IsComplete() && c.IsFailure() {
			failed = append(failed, c)
		}
	}
	return failed
}

// actionable decides whether an event should be acted on. Humans are always
// trusted; bots are acted on only if their login appears in the trusted list.
// This is the lesson from PR #50: a bot reviewer was confidently wrong about
// our golangci-lint config, and blindly applying its suggestion would have
// rewritten valid v2 syntax to broken v1.
func (w *Watcher) actionable(ev Event) bool {
	if github.IsNoctraReply(ev.Body) {
		return false
	}
	if ev.Type == EventComment && isBotDirectedCommand(ev.Body) {
		return false
	}
	if !ev.Author.IsBot() {
		return true
	}
	return w.trusted[strings.ToLower(ev.Author.Login)]
}

// botCommandRe matches a comment whose whole body is a bot-directed command
// ("@codex review", "@gemini", "/review") — meant for another tool, not Noctra.
// A comment mixing a command with real feedback won't match and is still acted on.
var botCommandRe = regexp.MustCompile(`(?i)^[@/][\w-]+(\s+(review|fix|fixup|please|now|again|go|run|retry|recheck))?$`)

func isBotDirectedCommand(body string) bool {
	return botCommandRe.MatchString(strings.TrimSpace(body))
}
