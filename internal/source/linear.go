package source

import (
	"context"
	"fmt"

	"github.com/ahmadAlMezaal/noctra/internal/linear"
)

// LinearConfig contains the Linear-specific settings needed by the adapter.
type LinearConfig struct {
	TeamKey          string
	TriggerMode      string
	TriggerState     string
	TriggerLabel     string
	InReviewState    string
	DoneState        string // optional, for auto-merge feature
	PlanConfirmLabel string
}

// LinearSource adapts the existing Linear client to TicketSource.
type LinearSource struct {
	client             *linear.Client
	cfg                LinearConfig
	states             linear.StateIDs
	triggerLabelID     string
	planConfirmLabelID string
}

func NewLinear(client *linear.Client, cfg LinearConfig) *LinearSource {
	return &LinearSource{client: client, cfg: cfg}
}

func (s *LinearSource) Name() string { return "linear" }

func (s *LinearSource) StateIDs() linear.StateIDs { return s.states }

func (s *LinearSource) TriggerLabelID() string { return s.triggerLabelID }

func (s *LinearSource) PlanConfirmLabelID() string { return s.planConfirmLabelID }

func (s *LinearSource) Prepare(ctx context.Context) error {
	triggerStateName := s.cfg.TriggerState
	if s.cfg.TriggerMode == "label" {
		triggerStateName = ""
	}
	states, err := s.client.ResolveStateIDs(ctx, s.cfg.TeamKey, triggerStateName, s.cfg.InReviewState, s.cfg.DoneState)
	if err != nil {
		return fmt.Errorf("resolve linear states: %w", err)
	}
	s.states = states

	if s.cfg.TriggerMode == "label" {
		lid, err := s.client.ResolveLabelID(ctx, s.cfg.TriggerLabel)
		if err != nil {
			return fmt.Errorf("resolve trigger label: %w", err)
		}
		s.triggerLabelID = lid
	}

	if s.cfg.PlanConfirmLabel != "" {
		lid, err := s.client.ResolveLabelID(ctx, s.cfg.PlanConfirmLabel)
		if err == nil {
			s.planConfirmLabelID = lid
		}
	}
	return nil
}

func (s *LinearSource) Fetch(ctx context.Context) ([]Ticket, error) {
	var (
		issues []linear.Issue
		err    error
	)
	if s.cfg.TriggerMode == "label" {
		issues, err = s.client.FetchLabeledIssues(ctx, s.cfg.TriggerLabel)
	} else {
		issues, err = s.client.FetchTriggerIssues(ctx, s.cfg.TriggerState)
	}
	if err != nil {
		return nil, err
	}
	return linearTickets(issues), nil
}

func (s *LinearSource) FetchByIdentifier(ctx context.Context, identifier string) (Ticket, error) {
	issue, err := s.client.GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		return Ticket{}, err
	}
	comments, err := s.client.FetchIssueComments(ctx, issue.ID)
	if err == nil {
		issue.Comments = linear.CommentConnection{Nodes: comments}
	}
	return linearTicket(issue), nil
}

func (s *LinearSource) FetchComments(ctx context.Context, ticket Ticket) ([]Comment, error) {
	comments, err := s.client.FetchIssueComments(ctx, ticket.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(comments))
	for _, c := range comments {
		out = append(out, linearComment(c))
	}
	return out, nil
}

func (s *LinearSource) RemovePlanLabel(ctx context.Context, ticket Ticket) error {
	if s.planConfirmLabelID == "" || !ticket.HasLabel(s.cfg.PlanConfirmLabel) {
		return nil
	}
	return s.client.RemoveLabel(ctx, ticket.ID, s.planConfirmLabelID)
}

func (s *LinearSource) BackToTrigger(ctx context.Context, ticket Ticket, body string) error {
	var firstErr error
	if s.states.Trigger != "" {
		if err := s.client.SetState(ctx, ticket.ID, s.states.Trigger); err != nil {
			firstErr = err
		}
	}
	if err := s.client.Comment(ctx, ticket.ID, body); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *LinearSource) MarkReady(ctx context.Context, ticket Ticket, info ReadyInfo) error {
	var firstErr error
	if s.cfg.TriggerMode == "label" && s.triggerLabelID != "" {
		if err := s.client.RemoveLabel(ctx, ticket.ID, s.triggerLabelID); err != nil {
			firstErr = err
		}
	}
	if err := s.client.SetState(ctx, ticket.ID, s.states.InReview); err != nil && firstErr == nil {
		firstErr = err
	}
	body := fmt.Sprintf(
		"🌙 **Noctra created a PR** (via %s)\n\n**PR:** %s\n\nMoved to **%s**. Ready for your review!",
		info.BackendLabel, info.PRURL, info.ReviewState)
	if err := s.client.Comment(ctx, ticket.ID, body); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *LinearSource) Comment(ctx context.Context, ticket Ticket, body string) error {
	return s.client.Comment(ctx, ticket.ID, body)
}

func linearTickets(issues []linear.Issue) []Ticket {
	out := make([]Ticket, 0, len(issues))
	for _, issue := range issues {
		out = append(out, linearTicket(issue))
	}
	return out
}

func linearTicket(issue linear.Issue) Ticket {
	var repoRef, repoBranch string
	if issue.Project != nil {
		repoRef, repoBranch = issue.Project.RepoDirective()
	}
	labels := make([]Label, 0, len(issue.Labels.Nodes))
	for _, l := range issue.Labels.Nodes {
		labels = append(labels, Label{Name: l.Name})
	}
	comments := make([]Comment, 0, len(issue.Comments.Nodes))
	for _, c := range issue.Comments.Nodes {
		comments = append(comments, linearComment(c))
	}
	return Ticket{
		Source:      "linear",
		ID:          issue.ID,
		Identifier:  issue.Identifier,
		Title:       issue.Title,
		Description: issue.Description,
		URL:         issue.URL,
		ProjectName: issue.ProjectName(),
		RepoRef:     repoRef,
		RepoBranch:  repoBranch,
		Comments:    comments,
		Labels:      labels,
		SourceData:  issue,
	}
}

func linearComment(c linear.Comment) Comment {
	author := ""
	if c.User != nil {
		author = c.User.Name
	}
	return Comment{Body: c.Body, Author: author}
}
