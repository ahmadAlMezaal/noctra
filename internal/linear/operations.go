package linear

import (
	"context"
	"fmt"
	"sort"
)

// StateIDs holds the resolved workflow-state IDs Noctra moves a team's tickets between.
type StateIDs struct {
	Trigger  string
	InReview string
	Done     string
}

// WorkflowStateID is a Linear workflow state's ID and name, for resolving user-supplied state names.
type WorkflowStateID struct {
	ID   string
	Name string
}

// ResolveStateIDs resolves a team's trigger/in-review/done state IDs; empty triggerName (label mode) and missing doneName are optional, in-review is required.
func (c *Client) ResolveStateIDs(ctx context.Context, teamKey, triggerName, inReviewName, doneName string) (StateIDs, error) {
	states, err := c.TeamWorkflowStates(ctx, teamKey)
	if err != nil {
		return StateIDs{}, err
	}

	var ids StateIDs
	available := make([]string, 0, len(states))
	for _, s := range states {
		available = append(available, s.Name)
		switch s.Name {
		case triggerName:
			ids.Trigger = s.ID
		case inReviewName:
			ids.InReview = s.ID
		}
		if doneName != "" && s.Name == doneName {
			ids.Done = s.ID
		}
	}
	if triggerName != "" && ids.Trigger == "" {
		return StateIDs{}, fmt.Errorf("state %q not found in team %q (available: %v)",
			triggerName, teamKey, available)
	}
	if ids.InReview == "" {
		return StateIDs{}, fmt.Errorf("state %q not found in team %q (available: %v)",
			inReviewName, teamKey, available)
	}
	return ids, nil
}

// TeamWorkflowStates returns every workflow state for the given Linear team.
func (c *Client) TeamWorkflowStates(ctx context.Context, teamKey string) ([]WorkflowStateID, error) {
	query := `{ teams { nodes { key states { nodes { id name } } } } }`

	var resp struct {
		Teams struct {
			Nodes []struct {
				Key    string `json:"key"`
				States struct {
					Nodes []WorkflowStateID `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}

	if err := c.Do(ctx, query, nil, &resp); err != nil {
		return nil, err
	}

	for _, team := range resp.Teams.Nodes {
		if team.Key == teamKey {
			return team.States.Nodes, nil
		}
	}

	var teams []string
	for _, t := range resp.Teams.Nodes {
		teams = append(teams, t.Key)
	}
	return nil, fmt.Errorf("team %q not found (available: %v)", teamKey, teams)
}

// ResolveStateID looks up one workflow state ID by exact name, also returning the available names for error context.
func (c *Client) ResolveStateID(ctx context.Context, teamKey, stateName string) (string, []string, error) {
	states, err := c.TeamWorkflowStates(ctx, teamKey)
	if err != nil {
		return "", nil, err
	}
	available := make([]string, 0, len(states))
	for _, s := range states {
		available = append(available, s.Name)
	}
	for _, s := range states {
		if s.Name == stateName {
			return s.ID, available, nil
		}
	}
	return "", available, fmt.Errorf("state %q not found in team %q", stateName, teamKey)
}

// FetchTriggerIssues returns every issue in the named state across all visible teams.
func (c *Client) FetchTriggerIssues(ctx context.Context, stateName string) ([]Issue, error) {
	query := `query($state: String!) {
	  teams { nodes { issues(filter: { state: { name: { eq: $state } } }, orderBy: updatedAt, first: 20) {
	    nodes { id identifier title description url project { name description content } comments(last: 50) { nodes { body user { name } } } labels { nodes { name } } }
	  } } }
	}`

	var resp struct {
		Teams struct {
			Nodes []struct {
				Issues struct {
					Nodes []Issue `json:"nodes"`
				} `json:"issues"`
			} `json:"nodes"`
		} `json:"teams"`
	}

	if err := c.Do(ctx, query, map[string]any{"state": stateName}, &resp); err != nil {
		return nil, err
	}

	var out []Issue
	for _, team := range resp.Teams.Nodes {
		out = append(out, team.Issues.Nodes...)
	}
	return out, nil
}

// StateCount is a project's issue count for one workflow state, with its type and board position for ordered rendering.
type StateCount struct {
	State    string
	Type     string
	Position float64
	Count    int
}

// ProjectIssueCounts returns the project's issue count per workflow state, ordered by board position; unknown/empty projects yield an empty slice, no error.
func (c *Client) ProjectIssueCounts(ctx context.Context, projectName string) ([]StateCount, error) {
	query := `query($project: String!, $after: String) {
	  issues(filter: { project: { name: { eq: $project } } }, first: 250, after: $after) {
	    nodes { state { name type position } }
	    pageInfo { hasNextPage endCursor }
	  }
	}`

	type agg struct {
		typ      string
		position float64
		count    int
	}
	byState := map[string]*agg{}

	after := ""
	for {
		var resp struct {
			Issues struct {
				Nodes []struct {
					State *struct {
						Name     string  `json:"name"`
						Type     string  `json:"type"`
						Position float64 `json:"position"`
					} `json:"state"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"issues"`
		}

		vars := map[string]any{"project": projectName}
		if after != "" {
			vars["after"] = after
		}
		if err := c.Do(ctx, query, vars, &resp); err != nil {
			return nil, err
		}

		for _, n := range resp.Issues.Nodes {
			if n.State == nil {
				continue
			}
			a := byState[n.State.Name]
			if a == nil {
				a = &agg{typ: n.State.Type, position: n.State.Position}
				byState[n.State.Name] = a
			}
			a.count++
		}

		if !resp.Issues.PageInfo.HasNextPage || resp.Issues.PageInfo.EndCursor == "" {
			break
		}
		after = resp.Issues.PageInfo.EndCursor
	}

	out := make([]StateCount, 0, len(byState))
	for name, a := range byState {
		out = append(out, StateCount{State: name, Type: a.typ, Position: a.position, Count: a.count})
	}
	sortStateCounts(out)
	return out, nil
}

// sortStateCounts orders by board position, then name for deterministic ties.
func sortStateCounts(s []StateCount) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Position != s[j].Position {
			return s[i].Position < s[j].Position
		}
		return s[i].State < s[j].State
	})
}

// SetState moves an issue to the given workflow state ID.
func (c *Client) SetState(ctx context.Context, issueID, stateID string) error {
	mutation := `mutation($id: String!, $stateId: String!) {
	  issueUpdate(id: $id, input: { stateId: $stateId }) { success }
	}`

	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.Do(ctx, mutation, map[string]any{"id": issueID, "stateId": stateID}, &resp); err != nil {
		return err
	}
	if !resp.IssueUpdate.Success {
		return fmt.Errorf("issueUpdate reported success=false for %s", issueID)
	}
	return nil
}

// ArchiveIssue archives an issue (reversible — restorable from the Linear UI).
func (c *Client) ArchiveIssue(ctx context.Context, issueID string) error {
	mutation := `mutation($id: String!) {
	  issueArchive(id: $id) { success }
	}`

	var resp struct {
		IssueArchive struct {
			Success bool `json:"success"`
		} `json:"issueArchive"`
	}
	if err := c.Do(ctx, mutation, map[string]any{"id": issueID}, &resp); err != nil {
		return err
	}
	if !resp.IssueArchive.Success {
		return fmt.Errorf("issueArchive reported success=false for %s", issueID)
	}
	return nil
}

// Comment posts a markdown comment on an issue.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	mutation := `mutation($issueId: String!, $body: String!) {
	  commentCreate(input: { issueId: $issueId, body: $body }) { success }
	}`

	var resp struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := c.Do(ctx, mutation, map[string]any{"issueId": issueID, "body": body}, &resp); err != nil {
		return err
	}
	if !resp.CommentCreate.Success {
		return fmt.Errorf("commentCreate reported success=false for %s", issueID)
	}
	return nil
}

// GetIssueByIdentifier fetches one issue by its identifier (e.g. "ENG-42"); Linear's issue(id:) accepts identifiers as well as UUIDs.
func (c *Client) GetIssueByIdentifier(ctx context.Context, identifier string) (Issue, error) {
	query := `query($id: String!) {
	  issue(id: $id) { id identifier title description url project { name } team { key } state { name type } assignee { name } labels { nodes { name } } }
	}`

	var resp struct {
		Issue *Issue `json:"issue"`
	}
	if err := c.Do(ctx, query, map[string]any{"id": identifier}, &resp); err != nil {
		return Issue{}, err
	}
	if resp.Issue == nil {
		return Issue{}, fmt.Errorf("linear returned no issue for identifier %q", identifier)
	}
	return *resp.Issue, nil
}

// ListProjectIssues returns up to limit project issues (newest first), optionally restricted to an exact stateName (empty = all states).
func (c *Client) ListProjectIssues(ctx context.Context, projectName, stateName string, limit int) ([]Issue, error) {
	if limit <= 0 {
		limit = 25
	}

	filter := "project: { name: { eq: $project } }"
	varDecl := "$project: String!, $limit: Int!"
	vars := map[string]any{"project": projectName, "limit": limit}
	if stateName != "" {
		filter += ", state: { name: { eq: $state } }"
		varDecl += ", $state: String!"
		vars["state"] = stateName
	}

	query := fmt.Sprintf(`query(%s) {
	  issues(filter: { %s }, orderBy: updatedAt, first: $limit) {
	    nodes { id identifier title url project { name } state { name type } }
	  }
	}`, varDecl, filter)

	var resp struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.Do(ctx, query, vars, &resp); err != nil {
		return nil, err
	}
	return resp.Issues.Nodes, nil
}

// SearchIssues returns up to limit issues whose title/description contains term (case-insensitive), newest first.
func (c *Client) SearchIssues(ctx context.Context, term string, limit int) ([]Issue, error) {
	if limit <= 0 {
		limit = 15
	}
	query := `query($q: String!, $limit: Int!) {
	  issues(filter: { or: [ { title: { containsIgnoreCase: $q } }, { description: { containsIgnoreCase: $q } } ] }, orderBy: updatedAt, first: $limit) {
	    nodes { id identifier title url project { name } state { name type } }
	  }
	}`

	var resp struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.Do(ctx, query, map[string]any{"q": term, "limit": limit}, &resp); err != nil {
		return nil, err
	}
	return resp.Issues.Nodes, nil
}

// ListProjects returns every visible project with its description/content.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	query := `query($after: String) {
	  projects(first: 250, after: $after) {
	    nodes { name description content }
	    pageInfo { hasNextPage endCursor }
	  }
	}`

	var out []Project
	after := ""
	for {
		var resp struct {
			Projects struct {
				Nodes    []Project `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"projects"`
		}
		vars := map[string]any{}
		if after != "" {
			vars["after"] = after
		}
		if err := c.Do(ctx, query, vars, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Projects.Nodes...)
		if !resp.Projects.PageInfo.HasNextPage || resp.Projects.PageInfo.EndCursor == "" {
			break
		}
		after = resp.Projects.PageInfo.EndCursor
	}
	return out, nil
}

// FetchLabeledIssues returns every issue carrying the named label across all visible teams (label-mode counterpart of FetchTriggerIssues).
func (c *Client) FetchLabeledIssues(ctx context.Context, labelName string) ([]Issue, error) {
	query := `query($label: String!) {
	  teams { nodes { issues(filter: { labels: { name: { eq: $label } } }, orderBy: updatedAt, first: 20) {
	    nodes { id identifier title description url project { name description content } comments(last: 50) { nodes { body user { name } } } labels { nodes { name } } }
	  } } }
	}`

	var resp struct {
		Teams struct {
			Nodes []struct {
				Issues struct {
					Nodes []Issue `json:"nodes"`
				} `json:"issues"`
			} `json:"nodes"`
		} `json:"teams"`
	}

	if err := c.Do(ctx, query, map[string]any{"label": labelName}, &resp); err != nil {
		return nil, err
	}

	var out []Issue
	for _, team := range resp.Teams.Nodes {
		out = append(out, team.Issues.Nodes...)
	}
	return out, nil
}

// ResolveLabelID looks up a label ID by name, erroring with the available labels if not found.
func (c *Client) ResolveLabelID(ctx context.Context, labelName string) (string, error) {
	query := `{ issueLabels(first: 250) { nodes { id name } } }`

	var resp struct {
		IssueLabels struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}
	if err := c.Do(ctx, query, nil, &resp); err != nil {
		return "", err
	}

	var available []string
	for _, l := range resp.IssueLabels.Nodes {
		if l.Name == labelName {
			return l.ID, nil
		}
		available = append(available, l.Name)
	}
	return "", fmt.Errorf("label %q not found (available: %v)", labelName, available)
}

// RemoveLabel drops one label from an issue by writing back its current set minus the target.
func (c *Client) RemoveLabel(ctx context.Context, issueID, labelID string) error {
	fetchQ := `query($id: String!) {
	  issue(id: $id) { labels { nodes { id } } }
	}`
	var fetchResp struct {
		Issue *struct {
			Labels struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"issue"`
	}
	if err := c.Do(ctx, fetchQ, map[string]any{"id": issueID}, &fetchResp); err != nil {
		return fmt.Errorf("fetch labels for %s: %w", issueID, err)
	}
	if fetchResp.Issue == nil {
		return fmt.Errorf("issue %s not found", issueID)
	}

	var remaining []string
	for _, l := range fetchResp.Issue.Labels.Nodes {
		if l.ID != labelID {
			remaining = append(remaining, l.ID)
		}
	}

	mutation := `mutation($id: String!, $labelIds: [String!]!) {
	  issueUpdate(id: $id, input: { labelIds: $labelIds }) { success }
	}`
	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.Do(ctx, mutation, map[string]any{"id": issueID, "labelIds": remaining}, &resp); err != nil {
		return err
	}
	if !resp.IssueUpdate.Success {
		return fmt.Errorf("issueUpdate reported success=false removing label from %s", issueID)
	}
	return nil
}

// AddLabel adds one label to an issue (no-op if already present) by writing back its current set plus the target.
func (c *Client) AddLabel(ctx context.Context, issueID, labelID string) error {
	fetchQ := `query($id: String!) {
	  issue(id: $id) { labels { nodes { id } } }
	}`
	var fetchResp struct {
		Issue *struct {
			Labels struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"issue"`
	}
	if err := c.Do(ctx, fetchQ, map[string]any{"id": issueID}, &fetchResp); err != nil {
		return fmt.Errorf("fetch labels for %s: %w", issueID, err)
	}
	if fetchResp.Issue == nil {
		return fmt.Errorf("issue %s not found", issueID)
	}

	ids := make([]string, 0, len(fetchResp.Issue.Labels.Nodes)+1)
	for _, l := range fetchResp.Issue.Labels.Nodes {
		if l.ID == labelID {
			return nil
		}
		ids = append(ids, l.ID)
	}
	ids = append(ids, labelID)

	mutation := `mutation($id: String!, $labelIds: [String!]!) {
	  issueUpdate(id: $id, input: { labelIds: $labelIds }) { success }
	}`
	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.Do(ctx, mutation, map[string]any{"id": issueID, "labelIds": ids}, &resp); err != nil {
		return err
	}
	if !resp.IssueUpdate.Success {
		return fmt.Errorf("issueUpdate reported success=false adding label to %s", issueID)
	}
	return nil
}

// FetchIssueComments returns an issue's most recent comments (up to 50); the plan-confirm flow (ENG-221) uses it to scan for approvals.
func (c *Client) FetchIssueComments(ctx context.Context, issueID string) ([]Comment, error) {
	query := `query($id: String!) {
	  issue(id: $id) { comments(last: 50) { nodes { body user { name } } } }
	}`

	var resp struct {
		Issue *struct {
			Comments struct {
				Nodes []Comment `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := c.Do(ctx, query, map[string]any{"id": issueID}, &resp); err != nil {
		return nil, err
	}
	if resp.Issue == nil {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}
	return resp.Issue.Comments.Nodes, nil
}

// Ping verifies auth by fetching the viewer, returning its display name.
func (c *Client) Ping(ctx context.Context) (string, error) {
	var resp struct {
		Viewer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"viewer"`
	}
	if err := c.Do(ctx, `{ viewer { id name } }`, nil, &resp); err != nil {
		return "", err
	}
	if resp.Viewer.ID == "" {
		return "", fmt.Errorf("linear returned no viewer — is the API key valid?")
	}
	return resp.Viewer.Name, nil
}
