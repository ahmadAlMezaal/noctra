// Package linear is a small Linear GraphQL client that covers the operations
// Noctra needs: fetching tickets in a trigger state, moving a ticket
// between states, and posting comments.
package linear

import (
	"fmt"
	"regexp"
	"strings"
)

// Project is the Linear project a ticket belongs to. Noctra routes the
// ticket to a repo by a "Repo:" directive in the project's content/description.
type Project struct {
	Name string `json:"name"`
	// Description is Linear's SHORT project description (GraphQL `description`).
	Description string `json:"description,omitempty"`
	// Content is the project's markdown BODY (GraphQL `content`) — this is where
	// a human writes the multi-line `Repo:` directive, so it's the field
	// RepoDirective reads first.
	Content string `json:"content,omitempty"`
}

var (
	repoDirectiveRe   = regexp.MustCompile(`(?im)^\s*Repo:\s*(.+?)\s*$`)
	branchDirectiveRe = regexp.MustCompile(`(?im)^\s*Branch:\s*(.+?)\s*$`)
)

// RepoDirective parses a "Repo: <owner/name | url>" line (and an optional
// "Branch: <name>" line) from the project content/description, letting a Linear
// project declare its target repo. Returns ("","") when no Repo line is
// present. branch is ignored unless a repo is also given.
func (p *Project) RepoDirective() (string, string) {
	if p == nil {
		return "", ""
	}
	// The directive lives in the project's markdown body (GraphQL `content`);
	// fall back to the short `description` in case it was written there instead.
	for _, src := range []string{p.Content, p.Description} {
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

// WorkflowState is the column a ticket sits in (e.g. "Next", "In Review") and
// its type ("backlog", "started", "completed", …).
type WorkflowState struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Team is the Linear team that owns an issue.
type Team struct {
	Key string `json:"key"`
}

// User is the subset of a Linear user Noctra surfaces (the assignee).
type User struct {
	Name string `json:"name"`
}

// Label is a Linear label attached to an issue.
type Label struct {
	Name string `json:"name"`
}

// LabelConnection is the GraphQL connection wrapper around an issue's labels.
type LabelConnection struct {
	Nodes []Label `json:"nodes"`
}

// Comment is a single Linear comment. Body is Markdown; User is the author
// (nil for app/integration comments). Only populated by queries that request
// comments — the trigger fetches — and empty otherwise.
type Comment struct {
	Body string `json:"body"`
	User *User  `json:"user,omitempty"`
}

// CommentConnection is the GraphQL connection wrapper around an issue's comments.
type CommentConnection struct {
	Nodes []Comment `json:"nodes"`
}

// Issue is the subset of a Linear issue Noctra acts on. State and Assignee
// are only populated by the read queries that request them (e.g.
// GetIssueByIdentifier); they are nil otherwise. Comments is only populated by
// the trigger fetches (FetchTriggerIssues / FetchLabeledIssues). Labels is
// populated by the trigger fetches and GetIssueByIdentifier.
type Issue struct {
	ID          string            `json:"id"`
	Identifier  string            `json:"identifier"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	URL         string            `json:"url"`
	Project     *Project          `json:"project,omitempty"`
	Team        *Team             `json:"team,omitempty"`
	State       *WorkflowState    `json:"state,omitempty"`
	Assignee    *User             `json:"assignee,omitempty"`
	Comments    CommentConnection `json:"comments,omitempty"`
	Labels      LabelConnection   `json:"labels,omitempty"`
}

// systemCommentMarkers identify comments that Noctra (or the Linear↔GitHub
// sync) posted automatically. They must never be fed back to the agent as human
// clarification — otherwise the agent's own BLOCKED notice would be echoed
// straight back at it. Every Noctra status comment is bold-prefixed with
// "**Noctra", which a human reply would not be.
//
// "**Nightshift" is retained for backward compatibility: tickets that were
// active before the Noctra rename (ENG-204) still carry automated comments
// with the old prefix, and those must keep being filtered so a re-dispatched
// ticket doesn't feed the agent its own pre-rename BLOCKED/status notices.
var systemCommentMarkers = []string{
	"**Noctra",
	"**Nightshift",
	"This comment thread is synced",
}

// IsSystemComment reports whether a comment body was posted automatically by
// Noctra (or the Linear↔GitHub sync) and should be excluded from human
// clarifications.
func IsSystemComment(body string) bool {
	// Classify only by the first non-empty line. A human who quotes one of our
	// notifications (a "> 🚧 **Noctra…" block) and then adds their own reply
	// must still count as a clarification — so a leading ">" quote is never a
	// system comment, and a marker buried later in the body doesn't trip it.
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

// ClarificationComments returns the human-authored comments on the issue,
// formatted as "Author: body", with Noctra's own automated notifications
// and the GitHub-sync notice filtered out. The implement prompt includes these
// so a human can unblock a ticket by replying in the comments — which is exactly
// what the BLOCKED notification instructs them to do.
func (i Issue) ClarificationComments() []string {
	var out []string
	for _, c := range i.Comments.Nodes {
		body := strings.TrimSpace(c.Body)
		if body == "" || IsSystemComment(body) {
			continue
		}
		author := "Someone"
		if c.User != nil && c.User.Name != "" {
			author = c.User.Name
		}
		out = append(out, fmt.Sprintf("%s: %s", author, body))
	}
	return out
}

// ProjectName returns the issue's project name, or "" if the ticket has no
// project attached.
func (i Issue) ProjectName() string {
	if i.Project == nil {
		return ""
	}
	return i.Project.Name
}

// StateName returns the issue's workflow-state name, or "" if not loaded.
func (i Issue) StateName() string {
	if i.State == nil {
		return ""
	}
	return i.State.Name
}

// AssigneeName returns the issue's assignee name, or "" if unassigned/not loaded.
func (i Issue) AssigneeName() string {
	if i.Assignee == nil {
		return ""
	}
	return i.Assignee.Name
}

// PlanConfirmCommentPrefix identifies Noctra's plan-confirm comments so they
// can be distinguished from other system comments by the approval scanner.
const PlanConfirmCommentPrefix = "📋 **Noctra: Implementation plan**"

// HasLabel reports whether the issue carries a label with the given name
// (case-insensitive, trimmed).
func (i Issue) HasLabel(name string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	for _, l := range i.Labels.Nodes {
		if strings.ToLower(strings.TrimSpace(l.Name)) == target {
			return true
		}
	}
	return false
}

// IsApprovalComment reports whether a comment body constitutes human approval
// of a pending plan. Recognized signals: "go", "lgtm", "approved", "approve",
// "👍", or the standard GitHub/Slack emoji shortcodes for thumbs-up.
func IsApprovalComment(body string) bool {
	s := strings.ToLower(strings.TrimSpace(body))
	switch s {
	case "go", "lgtm", "approved", "approve", "👍", ":thumbsup:", ":+1:":
		return true
	}
	return false
}

// BackendLabelPrefix is the label-name prefix that selects a per-ticket
// coding-agent backend (e.g. "agent:codex" → backend "codex").
const BackendLabelPrefix = "agent:"

// BackendLabel extracts the backend name from the issue's labels by looking
// for one prefixed with "agent:" (e.g. "agent:codex" → "codex"). Returns ""
// when no such label is present or the suffix is empty/whitespace-only.
func (i Issue) BackendLabel() string {
	for _, l := range i.Labels.Nodes {
		name := strings.ToLower(strings.TrimSpace(l.Name))
		if strings.HasPrefix(name, BackendLabelPrefix) {
			if v := strings.TrimSpace(strings.TrimPrefix(name, BackendLabelPrefix)); v != "" {
				return v
			}
		}
	}
	return ""
}
