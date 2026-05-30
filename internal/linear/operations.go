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
// within the team identified by teamKey (e.g. "ENG").
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
		if ids.Trigger == "" {
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
