// Package github wraps the `gh` CLI for the operations Nightshift's watcher
// needs: listing PRs Nightshift authored, fetching their comments and reviews,
// and (later) pulling check-run status. Stays thin — types mirror what `gh`
// returns under --json, so decoding is JSON unmarshal straight onto these.
package github

import "time"

// Actor is the GitHub user/bot that authored a comment or review. `gh`
// returns `type: "User"|"Bot"` — Nightshift treats bots specially in the
// trusted-reviewer filter.
type Actor struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// IsBot reports whether the actor is a GitHub App / Bot account.
func (a Actor) IsBot() bool { return a.Type == "Bot" }

// PR is the lightweight view returned by `gh pr list`.
type PR struct {
	URL         string `json:"url"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
}

// Comment is a top-level PR conversation comment (not an inline review
// comment). `gh pr view --json comments` returns these.
type Comment struct {
	ID        string    `json:"id"`
	Author    Actor     `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
}

// Review is a submitted PR review. State is one of APPROVED,
// CHANGES_REQUESTED, COMMENTED, DISMISSED.
type Review struct {
	ID          string    `json:"id"`
	Author      Actor     `json:"author"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// ReviewComment is an inline review-thread comment attached to a specific
// file + line in the PR diff (e.g. a "Suggested change"). These are NOT
// returned by `gh pr view`; they come from the REST API
// repos/{owner}/{repo}/pulls/{n}/comments, hence the snake_case tags and the
// nested `user` object instead of `author`.
type ReviewComment struct {
	ID        int64     `json:"id"`
	Author    Actor     `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	URL       string    `json:"html_url"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
}

// Details is the full view of a PR Nightshift needs to decide whether to
// re-engage and how. Mirrors the JSON shape `gh pr view --json ...` returns.
type Details struct {
	URL      string    `json:"url"`
	Number   int       `json:"number"`
	State    string    `json:"state"` // OPEN | CLOSED | MERGED
	Comments []Comment `json:"comments"`
	Reviews  []Review  `json:"reviews"`
	// ReviewComments are inline review-thread comments, fetched separately
	// from the REST API and merged in by GetPR (gh pr view omits them).
	ReviewComments []ReviewComment `json:"-"`
}

// IsOpen reports whether the PR is open (not closed or merged).
func (d Details) IsOpen() bool { return d.State == "OPEN" }
