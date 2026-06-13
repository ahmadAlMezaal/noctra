// Package linear is a small Linear GraphQL client that covers the operations
// Nightshift needs: fetching tickets in a trigger state, moving a ticket
// between states, and posting comments.
package linear

import (
	"fmt"
	"strings"
)

// Project is the Linear project a ticket belongs to. Nightshift uses the name
// to look the target repo up in repos.json.
type Project struct {
	Name string `json:"name"`
}

// WorkflowState is the column a ticket sits in (e.g. "Next", "In Review") and
// its type ("backlog", "started", "completed", …).
type WorkflowState struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// User is the subset of a Linear user Nightshift surfaces (the assignee).
type User struct {
	Name string `json:"name"`
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

// Issue is the subset of a Linear issue Nightshift acts on. State and Assignee
// are only populated by the read queries that request them (e.g.
// GetIssueByIdentifier); they are nil otherwise. Comments is only populated by
// the trigger fetches (FetchTriggerIssues / FetchLabeledIssues).
type Issue struct {
	ID          string            `json:"id"`
	Identifier  string            `json:"identifier"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	URL         string            `json:"url"`
	Project     *Project          `json:"project,omitempty"`
	State       *WorkflowState    `json:"state,omitempty"`
	Assignee    *User             `json:"assignee,omitempty"`
	Comments    CommentConnection `json:"comments,omitempty"`
}

// systemCommentMarkers identify comments that Nightshift (or the Linear↔GitHub
// sync) posted automatically. They must never be fed back to the agent as human
// clarification — otherwise the agent's own BLOCKED notice would be echoed
// straight back at it. Every Nightshift status comment is bold-prefixed with
// "**Nightshift", which a human reply would not be.
var systemCommentMarkers = []string{
	"**Nightshift",
	"This comment thread is synced",
}

func isSystemComment(body string) bool {
	for _, m := range systemCommentMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// ClarificationComments returns the human-authored comments on the issue,
// formatted as "Author: body", with Nightshift's own automated notifications
// and the GitHub-sync notice filtered out. The implement prompt includes these
// so a human can unblock a ticket by replying in the comments — which is exactly
// what the BLOCKED notification instructs them to do.
func (i Issue) ClarificationComments() []string {
	var out []string
	for _, c := range i.Comments.Nodes {
		body := strings.TrimSpace(c.Body)
		if body == "" || isSystemComment(body) {
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
