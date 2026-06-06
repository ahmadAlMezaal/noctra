// Package linear is a small Linear GraphQL client that covers the operations
// Nightshift needs: fetching tickets in a trigger state, moving a ticket
// between states, and posting comments.
package linear

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

// Issue is the subset of a Linear issue Nightshift acts on. State and Assignee
// are only populated by the read queries that request them (e.g.
// GetIssueByIdentifier); they are nil otherwise.
type Issue struct {
	ID          string         `json:"id"`
	Identifier  string         `json:"identifier"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	Project     *Project       `json:"project,omitempty"`
	State       *WorkflowState `json:"state,omitempty"`
	Assignee    *User          `json:"assignee,omitempty"`
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
