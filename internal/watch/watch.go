package watch

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/onelastcommit/noctra/internal/github"
	"github.com/onelastcommit/noctra/internal/state"
)

type EventType string

const (
	EventComment EventType = "comment"
	EventReview  EventType = "review"
)

type Event struct {
	Type        EventType
	Author      github.Actor
	Body        string
	URL         string
	At          time.Time
	ReviewState string
	Path        string
	Line        int
	CommentID   string
}

type CIFailure struct {
	SHA          string
	FailedChecks []github.Check
}

type PRChanges struct {
	PR            github.PR
	Details       *github.Details
	Events        []Event
	Skipped       []Event
	NewestComment time.Time
	NewestReview  time.Time
	CIFailure     *CIFailure
}

type Watcher struct {
	gh      *github.Client
	store   *state.Store
	trusted map[string]bool
	reposFn func(context.Context) []string
}

func New(gh *github.Client, store *state.Store, reposFn func(context.Context) []string, trusted []string) *Watcher {
	t := make(map[string]bool, len(trusted))
	for _, login := range trusted {
		if login = strings.ToLower(strings.TrimSpace(login)); login != "" {
			t[login] = true
		}
	}
	return &Watcher{gh: gh, store: store, trusted: t, reposFn: reposFn}
}

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
		cursorMoved := ch.NewestComment.After(cursor.LastCommentAt) ||
			ch.NewestReview.After(cursor.LastReviewAt)
		if len(ch.Events) > 0 || len(ch.Skipped) > 0 || cursorMoved || ch.CIFailure != nil {
			out = append(out, ch)
		}
	}
	return out, nil
}

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

		if r.State == "APPROVED" || r.State == "DISMISSED" {
			continue
		}
		if r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
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

	if d.HeadRefOid != "" && d.HeadRefOid != cursor.LastCISHA {
		if failed := failedChecks(d.StatusCheckRollup); len(failed) > 0 {
			out.CIFailure = &CIFailure{SHA: d.HeadRefOid, FailedChecks: failed}
		}
	}

	return out
}

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

var botCommandRe = regexp.MustCompile(`(?i)^[@/][\w-]+(\s+(review|fix|fixup|please|now|again|go|run|retry|recheck))?$`)

func isBotDirectedCommand(body string) bool {
	return botCommandRe.MatchString(strings.TrimSpace(body))
}
