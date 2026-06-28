// Package linear is a small Linear GraphQL client: fetch trigger-state tickets, move states, post comments.
package linear

import (
	"fmt"
	"regexp"
	"strings"
)

// Project is a ticket's Linear project; a "Repo:" directive in its content/description routes the repo.
type Project struct {
	Name string `json:"name"`
	// Description is Linear's short project description (GraphQL `description`).
	Description string `json:"description,omitempty"`
	// Content is the project's markdown body (GraphQL `content`) where the `Repo:` directive lives; RepoDirective reads it first.
	Content string `json:"content,omitempty"`
}

var (
	repoDirectiveRe   = regexp.MustCompile(`(?im)^\s*Repo:\s*(.+?)\s*$`)
	branchDirectiveRe = regexp.MustCompile(`(?im)^\s*Branch:\s*(.+?)\s*$`)
)

// RepoDirective parses a "Repo: <owner/name | url>" line (+ optional "Branch:") from content/description; returns ("","") if absent. branch is ignored without a repo.
func (p *Project) RepoDirective() (string, string) {
	if p == nil {
		return "", ""
	}
	// Prefer the markdown body (content); fall back to the short description.
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

// WorkflowState is a ticket's column (e.g. "Next") and its type ("backlog", "started", "completed", …).
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

// LabelConnection wraps an issue's labels (GraphQL connection).
type LabelConnection struct {
	Nodes []Label `json:"nodes"`
}

// Comment is a single Linear comment (Markdown body; User nil for app/integration comments); only populated by the trigger fetches.
type Comment struct {
	Body string `json:"body"`
	User *User  `json:"user,omitempty"`
}

// CommentConnection wraps an issue's comments (GraphQL connection).
type CommentConnection struct {
	Nodes []Comment `json:"nodes"`
}

// Issue is the subset of a Linear issue Noctra acts on; State/Assignee/Comments/Labels are nil unless the query requested them.
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

// systemCommentMarkers identify auto-posted comments (Noctra status / Linear↔GitHub sync) so they aren't fed back to the agent as clarification.
// "**Nightshift" stays for backward compat: pre-rename (ENG-204) tickets carry the old prefix and must keep being filtered.
var systemCommentMarkers = []string{
	"**Noctra",
	"**Nightshift",
	"This comment thread is synced",
}

// IsSystemComment reports whether a comment body was auto-posted (and so excluded from human clarifications).
func IsSystemComment(body string) bool {
	// Classify only by the first non-empty line: a leading ">" quote (a human quoting our notice, then replying) is never a system comment.
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

// ClarificationComments returns human-authored comments as "Author: body" (auto-posted notices filtered) so a human reply can unblock a ticket via the implement prompt.
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

// ProjectName returns the issue's project name, or "" if unattached.
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

// AssigneeName returns the assignee name, or "" if unassigned/not loaded.
func (i Issue) AssigneeName() string {
	if i.Assignee == nil {
		return ""
	}
	return i.Assignee.Name
}

// PlanConfirmCommentPrefix marks Noctra's plan-confirm comments so the approval scanner can spot them.
const PlanConfirmCommentPrefix = "📋 **Noctra: Implementation plan**"

// HasLabel reports whether the issue carries the named label (case-insensitive, trimmed).
func (i Issue) HasLabel(name string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	for _, l := range i.Labels.Nodes {
		if strings.ToLower(strings.TrimSpace(l.Name)) == target {
			return true
		}
	}
	return false
}

// IsApprovalComment reports whether a comment body approves a pending plan ("go"/"lgtm"/"approve(d)"/👍/thumbs-up shortcodes).
func IsApprovalComment(body string) bool {
	s := strings.ToLower(strings.TrimSpace(body))
	switch s {
	case "go", "lgtm", "approved", "approve", "👍", ":thumbsup:", ":+1:":
		return true
	}
	return false
}

// BackendLabelPrefix is the label prefix selecting a per-ticket agent backend ("agent:codex" → "codex").
const BackendLabelPrefix = "agent:"

// BackendLabel returns the backend from an "agent:<name>" label, or "" if none/empty.
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
