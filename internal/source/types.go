// Package source defines the ticket-source boundary used by the pipeline.
package source

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// TicketSource is the source-specific surface the implementation pipeline
// needs. Adapters hide whether a ticket came from Linear, GitHub Issues, or a
// future source.
type TicketSource interface {
	Name() string
	Prepare(context.Context) error
	Fetch(context.Context) ([]Ticket, error)
	FetchByIdentifier(context.Context, string) (Ticket, error)
	FetchComments(context.Context, Ticket) ([]Comment, error)
	RemovePlanLabel(context.Context, Ticket) error
	BackToTrigger(context.Context, Ticket, string) error
	MarkReady(context.Context, Ticket, ReadyInfo) error
	Comment(context.Context, Ticket, string) error
}

// ReadyInfo is the source-agnostic status update after Noctra opens a PR.
type ReadyInfo struct {
	PRURL        string
	BackendLabel string
	ReviewState  string
}

// Comment is a human or system comment attached to a ticket.
type Comment struct {
	Body   string
	Author string
}

// Label is a source label attached to a ticket.
type Label struct {
	Name string
}

// Ticket is the source-neutral ticket shape consumed by the pipeline.
type Ticket struct {
	Source      string
	ID          string
	Identifier  string
	Title       string
	Description string
	URL         string
	ProjectName string
	RepoRef     string
	RepoBranch  string
	Comments    []Comment
	Labels      []Label

	// SourceData is owned by the adapter and lets it carry opaque fields
	// needed for mutations without leaking them into pipeline code.
	SourceData any
}

var (
	repoDirectiveRe   = regexp.MustCompile(`(?im)^\s*Repo:\s*(.+?)\s*$`)
	branchDirectiveRe = regexp.MustCompile(`(?im)^\s*Branch:\s*(.+?)\s*$`)
)

// ParseRepoDirective parses a "Repo: <owner/name | url>" line and optional
// "Branch: <name>" line from source-specific ticket/project text.
func ParseRepoDirective(texts ...string) (string, string) {
	for _, src := range texts {
		m := repoDirectiveRe.FindStringSubmatch(src)
		if m == nil {
			continue
		}
		r := strings.TrimSpace(m[1])
		if r == "" {
			continue
		}
		var b string
		if bm := branchDirectiveRe.FindStringSubmatch(src); bm != nil {
			b = strings.TrimSpace(bm[1])
		}
		return r, b
	}
	return "", ""
}

// systemCommentMarkers identify comments that Noctra (or sync tooling) posted
// automatically. They must not be fed back to the agent as human clarification.
var systemCommentMarkers = []string{
	"**Noctra",
	"**Nightshift",
	"This comment thread is synced",
}

// IsSystemComment reports whether a comment body was posted automatically by
// Noctra or sync tooling.
func IsSystemComment(body string) bool {
	firstLine := ""
	for _, line := range strings.Split(body, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			firstLine = s
			break
		}
	}
	if strings.HasPrefix(firstLine, ">") {
		return false
	}
	for _, m := range systemCommentMarkers {
		if strings.Contains(firstLine, m) {
			return true
		}
	}
	return false
}

// ClarificationComments returns human-authored ticket comments formatted for
// the agent prompt.
func (t Ticket) ClarificationComments() []string {
	var out []string
	for _, c := range t.Comments {
		body := strings.TrimSpace(c.Body)
		if body == "" || IsSystemComment(body) {
			continue
		}
		author := strings.TrimSpace(c.Author)
		if author == "" {
			author = "Someone"
		}
		out = append(out, fmt.Sprintf("%s: %s", author, body))
	}
	return out
}

// HasLabel reports whether the ticket carries the given label.
func (t Ticket) HasLabel(name string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	for _, l := range t.Labels {
		if strings.ToLower(strings.TrimSpace(l.Name)) == target {
			return true
		}
	}
	return false
}

// BackendLabelPrefix is the label-name prefix that selects a per-ticket
// coding-agent backend (e.g. "agent:codex" -> backend "codex").
const BackendLabelPrefix = "agent:"

// BackendLabel extracts the backend name from the ticket's labels.
func (t Ticket) BackendLabel() string {
	for _, l := range t.Labels {
		name := strings.ToLower(strings.TrimSpace(l.Name))
		if strings.HasPrefix(name, BackendLabelPrefix) {
			if v := strings.TrimSpace(strings.TrimPrefix(name, BackendLabelPrefix)); v != "" {
				return v
			}
		}
	}
	return ""
}

// PlanConfirmCommentPrefix identifies Noctra's plan-confirm comments.
const PlanConfirmCommentPrefix = "📋 **Noctra: Implementation plan**"

// IsApprovalComment reports whether a comment body constitutes human approval
// of a pending plan.
func IsApprovalComment(body string) bool {
	s := strings.ToLower(strings.TrimSpace(body))
	switch s {
	case "go", "lgtm", "approved", "approve", "👍", ":thumbsup:", ":+1:":
		return true
	}
	return false
}
