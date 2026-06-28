// Package github wraps the `gh` CLI for the watcher: listing Noctra-authored PRs, fetching comments/reviews/check status, and pulling failed-check logs. Types mirror `gh --json` so decoding is plain unmarshal.
package github

import "time"

// Actor is the user/bot that authored a comment or review (`type: "User"|"Bot"`); bots are gated by the trusted-reviewer filter.
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
	Body        string `json:"body"`

	// RepoURL is the discovering clone's remote URL (set by ListNoctraPRs, not gh JSON); preserves the scheme (e.g. SSH) so auto-iterate re-resolves over the same transport — synthesizing HTTPS from owner/name would fail on SSH-only private repos.
	RepoURL string `json:"-"`
}

// Comment is a top-level PR conversation comment (not inline), from `gh pr view --json comments`.
type Comment struct {
	ID        string    `json:"id"`
	Author    Actor     `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
}

// Review is a submitted PR review; State is APPROVED|CHANGES_REQUESTED|COMMENTED|DISMISSED.
type Review struct {
	ID          string    `json:"id"`
	Author      Actor     `json:"author"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// ReviewComment is an inline review-thread comment on a file+line; from the REST API (not `gh pr view`), hence snake_case tags and `user` instead of `author`.
type ReviewComment struct {
	ID        int64     `json:"id"`
	Author    Actor     `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	URL       string    `json:"html_url"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
}

// Check is one status-check-rollup entry; `statusCheckRollup` unions CheckRun (Actions) and StatusContext (legacy statuses) — this captures both, helpers normalise.
type Check struct {
	Typename string `json:"__typename"`
	// CheckRun fields.
	Name         string `json:"name"`
	Status       string `json:"status"`     // QUEUED | IN_PROGRESS | COMPLETED | PENDING
	Conclusion   string `json:"conclusion"` // SUCCESS | FAILURE | TIMED_OUT | CANCELLED | ACTION_REQUIRED | NEUTRAL | SKIPPED | STARTUP_FAILURE
	DetailsURL   string `json:"detailsUrl"`
	WorkflowName string `json:"workflowName"`
	// StatusContext fields.
	Context   string `json:"context"`
	State     string `json:"state"` // SUCCESS | FAILURE | ERROR | PENDING | EXPECTED
	TargetURL string `json:"targetUrl"`
}

// CheckName is the display name regardless of check kind.
func (c Check) CheckName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Context
}

// URL is the details/target link regardless of check kind.
func (c Check) URL() string {
	if c.DetailsURL != "" {
		return c.DetailsURL
	}
	return c.TargetURL
}

// IsComplete reports whether the check has finished (result final, worth acting on); in-flight checks are left alone.
func (c Check) IsComplete() bool {
	if c.Status != "" { // CheckRun
		return c.Status == "COMPLETED"
	}
	// StatusContext has no status; PENDING/EXPECTED mean in flight.
	return c.State != "" && c.State != "PENDING" && c.State != "EXPECTED"
}

// IsFailure reports whether a completed check failed in a way worth fixing.
func (c Check) IsFailure() bool {
	switch c.Conclusion { // CheckRun
	case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return true
	}
	switch c.State { // StatusContext
	case "FAILURE", "ERROR":
		return true
	}
	return false
}

// Details is the full PR view Noctra uses to decide whether/how to re-engage; mirrors `gh pr view --json ...`.
type Details struct {
	URL               string    `json:"url"`
	Number            int       `json:"number"`
	State             string    `json:"state"` // OPEN | CLOSED | MERGED
	HeadRefOid        string    `json:"headRefOid"`
	Comments          []Comment `json:"comments"`
	Reviews           []Review  `json:"reviews"`
	StatusCheckRollup []Check   `json:"statusCheckRollup"`
	// ReviewComments are inline review-thread comments, fetched from REST and merged in by GetPR (gh pr view omits them).
	ReviewComments []ReviewComment `json:"-"`
}

// IsOpen reports whether the PR is open (not closed or merged).
func (d Details) IsOpen() bool { return d.State == "OPEN" }
