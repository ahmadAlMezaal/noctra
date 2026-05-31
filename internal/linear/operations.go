package linear

import (
	"context"
	"fmt"
)

// StateIDs holds the resolved workflow-state IDs Nightshift moves tickets
// between for a given team.
type StateIDs struct {
	Trigger  string
	InReview string
}

// ResolveStateIDs looks up the IDs for the trigger and in-review state names
// within the team identified by teamKey (e.g. "ENG"). When triggerName is
// empty (label-based trigger mode), the trigger-state lookup is skipped —
// only the in-review state is required.
func (c *Client) ResolveStateIDs(ctx context.Context, teamKey, triggerName, inReviewName string) (StateIDs, error) {
	query := `{ teams { nodes { key states { nodes { id name } } } } }`

	var resp struct {
		Teams struct {
			Nodes []struct {
				Key    string `json:"key"`
				States struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}

	if err := c.Do(ctx, query, nil, &resp); err != nil {
		return StateIDs{}, err
	}

	for _, team := range resp.Teams.Nodes {
		if team.Key != teamKey {
			continue
		}
		var ids StateIDs
		var available []string
		for _, s := range team.States.Nodes {
			available = append(available, s.Name)
			switch s.Name {
			case triggerName:
				ids.Trigger = s.ID
			case inReviewName:
				ids.InReview = s.ID
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

	var teams []string
	for _, t := range resp.Teams.Nodes {
		teams = append(teams, t.Key)
	}
	return StateIDs{}, fmt.Errorf("team %q not found (available: %v)", teamKey, teams)
}

// FetchTriggerIssues returns every issue currently in the named state, across
// all teams the API key can see. The caller can filter by team key in code if
// needed.
func (c *Client) FetchTriggerIssues(ctx context.Context, stateName string) ([]Issue, error) {
	query := `query($state: String!) {
	  teams { nodes { issues(filter: { state: { name: { eq: $state } } }, orderBy: updatedAt, first: 20) {
	    nodes { id identifier title description url project { name } }
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

// GetIssueByIdentifier fetches a single issue by its human-readable
// identifier (e.g. "ENG-42"). Linear's API accepts identifiers in addition
// to UUIDs for the `issue(id:)` argument.
func (c *Client) GetIssueByIdentifier(ctx context.Context, identifier string) (Issue, error) {
	query := `query($id: String!) {
	  issue(id: $id) { id identifier title description url project { name } }
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

// FetchLabeledIssues returns every issue carrying the named label, across all
// teams the API key can see. This is the label-mode counterpart of
// FetchTriggerIssues.
func (c *Client) FetchLabeledIssues(ctx context.Context, labelName string) ([]Issue, error) {
	query := `query($label: String!) {
	  teams { nodes { issues(filter: { labels: { name: { eq: $label } } }, orderBy: updatedAt, first: 20) {
	    nodes { id identifier title description url project { name } }
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

// ResolveLabelID looks up the ID for a label by name. Returns an error listing
// available labels if the name is not found.
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

// RemoveLabel removes a single label from an issue. It fetches the issue's
// current labels, filters out the target, and writes the remaining set back.
func (c *Client) RemoveLabel(ctx context.Context, issueID, labelID string) error {
	// Fetch current labels.
	fetchQ := `query($id: String!) {
	  issue(id: $id) { labels { nodes { id } } }
	}`
	var fetchResp struct {
		Issue struct {
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

// AddLabel adds a single label to an issue. It fetches the issue's current
// labels, appends the target if not already present, and writes the full set
// back. No-ops if the issue already has the label.
func (c *Client) AddLabel(ctx context.Context, issueID, labelID string) error {
	fetchQ := `query($id: String!) {
	  issue(id: $id) { labels { nodes { id } } }
	}`
	var fetchResp struct {
		Issue struct {
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

	ids := make([]string, 0, len(fetchResp.Issue.Labels.Nodes)+1)
	for _, l := range fetchResp.Issue.Labels.Nodes {
		if l.ID == labelID {
			return nil // already labelled
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

// Ping verifies the API key works by fetching the authenticated viewer.
// Returns the viewer's display name on success.
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
