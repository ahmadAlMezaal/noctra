// Package watch classifies Noctra-authored PRs into PRChanges (new feedback since the state cursor); side-effect-free — the pipeline drives the response.
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

// EventType distinguishes comments from reviews for the prompt builder.
type EventType string

const (
	EventComment EventType = "comment"
	EventReview  EventType = "review"
)

// Event is one new piece of feedback on a PR — a conversation comment or a submitted review.
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

// CIFailure is a head commit whose CI completed with ≥1 failing check that Noctra hasn't re-engaged on yet.
type CIFailure struct {
	SHA          string         // head commit the failures belong to
	FailedChecks []github.Check // only the failing checks
}

// PRChanges is one PR's worth of new feedback; NewestComment/NewestReview are cursors to persist *after* the iteration succeeds.
type PRChanges struct {
	PR            github.PR
	Details       *github.Details
	Events        []Event    // actionable (action!)
	Skipped       []Event    // seen but filtered (log + ignore)
	NewestComment time.Time  // greatest CreatedAt across all comments
	NewestReview  time.Time  // greatest SubmittedAt across all reviews
	CIFailure     *CIFailure // non-nil when the head commit's CI failed and is unhandled
}

// Watcher couples the gh client with the state store and the trusted-reviewer allowlist.
type Watcher struct {
	gh      *github.Client
	store   *state.Store
	trusted map[string]bool
	// reposFn returns repo git URLs to scan; a func (not a slice) because directive routing clones on demand, so it's re-discovered each poll. May be nil (scans nothing).
	reposFn func(context.Context) []string
}

// New constructs a Watcher; trusted lists logins/bots to act on (humans always trusted, so it only restricts bots), reposFn is evaluated per Scan.
func New(gh *github.Client, store *state.Store, reposFn func(context.Context) []string, trusted []string) *Watcher {
	// GitHub logins are case-insensitive; normalise to lower to match the API's casing.
	t := make(map[string]bool, len(trusted))
	for _, login := range trusted {
		if login = strings.ToLower(strings.TrimSpace(login)); login != "" {
			t[login] = true
		}
	}
	return &Watcher{gh: gh, store: store, trusted: t, reposFn: reposFn}
}

// Scan returns one PRChanges per open Noctra PR with new events; unchanged PRs are omitted.
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
		// Return PRs whose only new events are non-actionable (APPROVED, empty COMMENTED) so the caller still persists the advanced cursor.
		cursorMoved := ch.NewestComment.After(cursor.LastCommentAt) ||
			ch.NewestReview.After(cursor.LastReviewAt)
		if len(ch.Events) > 0 || len(ch.Skipped) > 0 || cursorMoved || ch.CIFailure != nil {
			out = append(out, ch)
		}
	}
	return out, nil
}

// diff classifies comments+reviews past the cursor as actionable or skipped; at-or-before the cursor are ignored (handled in a prior poll).
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

	// Inline review-thread comments share the comment cursor — timestamps are globally ordered, so a new one always sorts after anything seen before.
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

		// Reviews needing no response: skip the prompt but still advance the cursor.
		if r.State == "APPROVED" || r.State == "DISMISSED" {
			continue
		}
		if r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
			// Empty COMMENTED reviews wrap inline comments; nothing actionable at review level.
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

	// CI: act once per head commit. Keyed by SHA not timestamp — a fix changes the SHA so a fresh failure is eligible again (bounded by the iteration cap).
	if d.HeadRefOid != "" && d.HeadRefOid != cursor.LastCISHA {
		if failed := failedChecks(d.StatusCheckRollup); len(failed) > 0 {
			out.CIFailure = &CIFailure{SHA: d.HeadRefOid, FailedChecks: failed}
		}
	}

	return out
}

// failedChecks returns checks in a final failing state — a stuck pending check shouldn't hide an already-final failure.
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

// actionable: humans always trusted; bots only if their login is in the trusted list (PR #50 lesson — a bot was confidently wrong about our golangci-lint v2 config).
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

// botCommandRe matches a body that's entirely a bot-directed command ("@codex review", "/review") — meant for another tool; a command mixed with real feedback won't match and is still acted on.
var botCommandRe = regexp.MustCompile(`(?i)^[@/][\w-]+(\s+(review|fix|fixup|please|now|again|go|run|retry|recheck))?$`)

func isBotDirectedCommand(body string) bool {
	return botCommandRe.MatchString(strings.TrimSpace(body))
}
