// Package linear is a small Linear GraphQL client that covers the operations
// Nightshift needs: fetching tickets in a trigger state, moving a ticket
// between states, and posting comments.
package linear

// Project is the Linear project a ticket belongs to. Nightshift uses the name
// to look the target repo up in repos.json.
type Project struct {
	Name string `json:"name"`
}

// Issue is the subset of a Linear issue Nightshift acts on.
type Issue struct {
	ID          string   `json:"id"`
	Identifier  string   `json:"identifier"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Project     *Project `json:"project,omitempty"`
}

// ProjectName returns the issue's project name, or "" if the ticket has no
// project attached.
func (i Issue) ProjectName() string {
	if i.Project == nil {
		return ""
	}
	return i.Project.Name
}
