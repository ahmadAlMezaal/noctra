package source

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// JiraConfig contains the settings for the Jira Cloud source.
type JiraConfig struct {
	BaseURL        string // e.g. "https://your-org.atlassian.net"
	UserEmail      string // Jira account email for basic auth
	APIToken       string // Jira API token (https://id.atlassian.com/manage-profile/security/api-tokens)
	Project        string // Jira project key, e.g. "PROJ"
	TriggerStatus  string // status name that triggers dispatch, e.g. "To Do"
	InReviewStatus string // status name after PR is opened, e.g. "In Review"
	TriggerLabel   string // optional: label that triggers dispatch instead of status
}

// JiraSource polls Jira Cloud for issues by status or label via REST API v3 with basic auth (email + API token).
type JiraSource struct {
	cfg    JiraConfig
	client *http.Client
}

func NewJira(cfg JiraConfig) *JiraSource {
	return &JiraSource{cfg: cfg, client: &http.Client{}}
}

func (s *JiraSource) Name() string { return "jira" }

func (s *JiraSource) Prepare(context.Context) error {
	if strings.TrimSpace(s.cfg.BaseURL) == "" {
		return fmt.Errorf("JIRA_BASE_URL is required when jira is an active ticket source")
	}
	if strings.TrimSpace(s.cfg.UserEmail) == "" {
		return fmt.Errorf("JIRA_USER_EMAIL is required when jira is an active ticket source")
	}
	if strings.TrimSpace(s.cfg.APIToken) == "" {
		return fmt.Errorf("JIRA_API_TOKEN is required when jira is an active ticket source")
	}
	if strings.TrimSpace(s.cfg.Project) == "" {
		return fmt.Errorf("JIRA_PROJECT is required when jira is an active ticket source")
	}
	if strings.TrimSpace(s.cfg.TriggerStatus) == "" && strings.TrimSpace(s.cfg.TriggerLabel) == "" {
		return fmt.Errorf("JIRA_TRIGGER_STATUS or JIRA_TRIGGER_LABEL is required when jira is an active ticket source")
	}
	if strings.TrimSpace(s.cfg.InReviewStatus) == "" {
		return fmt.Errorf("JIRA_IN_REVIEW_STATUS is required when jira is an active ticket source")
	}
	s.cfg.BaseURL = strings.TrimRight(s.cfg.BaseURL, "/")
	return nil
}

func (s *JiraSource) Fetch(ctx context.Context) ([]Ticket, error) {
	jql := s.buildFetchJQL()
	issues, err := s.searchIssues(ctx, jql)
	if err != nil {
		return nil, err
	}
	out := make([]Ticket, 0, len(issues))
	for _, issue := range issues {
		out = append(out, s.ticket(issue))
	}
	return out, nil
}

func (s *JiraSource) FetchByIdentifier(ctx context.Context, identifier string) (Ticket, error) {
	issue, err := s.getIssue(ctx, identifier)
	if err != nil {
		return Ticket{}, err
	}
	return s.ticket(issue), nil
}

func (s *JiraSource) FetchComments(ctx context.Context, ticket Ticket) ([]Comment, error) {
	comments, err := s.getComments(ctx, ticket.Identifier)
	if err != nil {
		return nil, err
	}
	return comments, nil
}

func (s *JiraSource) RemovePlanLabel(context.Context, Ticket) error {
	return nil
}

func (s *JiraSource) BackToTrigger(ctx context.Context, ticket Ticket, body string) error {
	// Status transitions are workflow-dependent, so only post a comment; the JQL trigger won't re-fetch unless still in trigger status.
	return s.Comment(ctx, ticket, body)
}

func (s *JiraSource) MarkReady(ctx context.Context, ticket Ticket, info ReadyInfo) error {
	var firstErr error
	if err := s.transitionTo(ctx, ticket.Identifier, s.cfg.InReviewStatus); err != nil {
		firstErr = err
	}
	if s.cfg.TriggerLabel != "" {
		if err := s.removeLabel(ctx, ticket.Identifier, s.cfg.TriggerLabel); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	body := fmt.Sprintf(
		"🌙 *Noctra created a PR* (via %s)\n\n*PR:* %s\n\nTransitioned to *%s*. Ready for your review!",
		info.BackendLabel, info.PRURL, info.ReviewState)
	if err := s.Comment(ctx, ticket, body); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *JiraSource) Comment(ctx context.Context, ticket Ticket, body string) error {
	return s.addComment(ctx, ticket.Identifier, body)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (s *JiraSource) buildFetchJQL() string {
	if s.cfg.TriggerLabel != "" {
		return fmt.Sprintf(`project = %q AND labels = %q AND statusCategory != "Done" ORDER BY created ASC`,
			s.cfg.Project, s.cfg.TriggerLabel)
	}
	return fmt.Sprintf(`project = %q AND status = %q ORDER BY created ASC`,
		s.cfg.Project, s.cfg.TriggerStatus)
}

// ticket converts a Jira issue to the source-neutral Ticket shape.
func (s *JiraSource) ticket(issue jiraIssue) Ticket {
	desc := issue.descriptionText()
	repoRef, repoBranch := ParseRepoDirective(desc)

	labels := make([]Label, 0, len(issue.Fields.Labels))
	for _, l := range issue.Fields.Labels {
		labels = append(labels, Label{Name: l})
	}

	comments := make([]Comment, 0, len(issue.Fields.Comment.Comments))
	for _, c := range issue.Fields.Comment.Comments {
		comments = append(comments, Comment{
			Body:   c.bodyText(),
			Author: c.Author.DisplayName,
		})
	}

	return Ticket{
		Source:      "jira",
		ID:          issue.ID,
		Identifier:  issue.Key,
		Title:       issue.Fields.Summary,
		Description: desc,
		URL:         s.cfg.BaseURL + "/browse/" + issue.Key,
		ProjectName: issue.Fields.Project.Key,
		RepoRef:     repoRef,
		RepoBranch:  repoBranch,
		Comments:    comments,
		Labels:      labels,
		SourceData: jiraMeta{
			BaseURL: s.cfg.BaseURL,
			Key:     issue.Key,
		},
	}
}

// ── Jira REST API types ───────────────────────────────────────────────────────

type jiraIssue struct {
	ID     string     `json:"id"`
	Key    string     `json:"key"`
	Fields jiraFields `json:"fields"`
}

type jiraFields struct {
	Summary     string           `json:"summary"`
	Description *jiraADFDocument `json:"description"` // ADF (Atlassian Document Format) or null
	Status      jiraStatus       `json:"status"`
	Project     jiraProject      `json:"project"`
	Labels      []string         `json:"labels"`
	Comment     jiraCommentPage  `json:"comment"`
}

type jiraStatus struct {
	Name string `json:"name"`
}

type jiraProject struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type jiraCommentPage struct {
	Comments []jiraComment `json:"comments"`
}

type jiraComment struct {
	Body   *jiraADFDocument `json:"body"` // ADF or null
	Author jiraUser         `json:"author"`
}

type jiraUser struct {
	DisplayName string `json:"displayName"`
}

// jiraADFDocument is a minimal Atlassian Document Format tree, flattened to plain text for the prompt / Repo: parsing.
type jiraADFDocument struct {
	Type    string           `json:"type"`
	Content []jiraADFContent `json:"content"`
}

type jiraADFContent struct {
	Type    string           `json:"type"`
	Text    string           `json:"text,omitempty"`
	Content []jiraADFContent `json:"content,omitempty"`
}

// descriptionText flattens the Jira ADF description to plain text.
func (issue jiraIssue) descriptionText() string {
	if issue.Fields.Description == nil {
		return ""
	}
	return adfToText(issue.Fields.Description)
}

// bodyText flattens an ADF comment body to plain text.
func (c jiraComment) bodyText() string {
	if c.Body == nil {
		return ""
	}
	return adfToText(c.Body)
}

// adfToText extracts plain text from an Atlassian Document Format tree.
func adfToText(doc *jiraADFDocument) string {
	if doc == nil {
		return ""
	}
	var b strings.Builder
	for _, node := range doc.Content {
		adfNodeText(&b, node)
	}
	return strings.TrimSpace(b.String())
}

func adfNodeText(b *strings.Builder, node jiraADFContent) {
	if node.Type == "hardBreak" {
		b.WriteByte('\n')
		return
	}
	if node.Text != "" {
		b.WriteString(node.Text)
	}
	for _, child := range node.Content {
		adfNodeText(b, child)
	}
	if node.Type == "paragraph" || node.Type == "heading" || node.Type == "listItem" || node.Type == "blockquote" || node.Type == "codeBlock" {
		s := b.String()
		if len(s) > 0 && s[len(s)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
}

type jiraMeta struct {
	BaseURL string
	Key     string
}

// ── Jira REST API calls ───────────────────────────────────────────────────────

func (s *JiraSource) authHeader() string {
	cred := s.cfg.UserEmail + ":" + s.cfg.APIToken
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(cred))
}

func (s *JiraSource) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := s.cfg.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", s.authHeader())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.client.Do(req)
}

func (s *JiraSource) searchIssues(ctx context.Context, jql string) ([]jiraIssue, error) {
	payload := map[string]any{
		"jql":        jql,
		"maxResults": 100,
		"fields":     []string{"summary", "description", "status", "project", "labels", "comment"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := s.doRequest(ctx, http.MethodPost, "/rest/api/3/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jira search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("jira search: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var result struct {
		Issues []jiraIssue `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("jira search: decode: %w", err)
	}
	return result.Issues, nil
}

func (s *JiraSource) getIssue(ctx context.Context, key string) (jiraIssue, error) {
	resp, err := s.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/rest/api/3/issue/%s?fields=summary,description,status,project,labels,comment", key), nil)
	if err != nil {
		return jiraIssue{}, fmt.Errorf("jira get issue %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return jiraIssue{}, fmt.Errorf("jira get issue %s: HTTP %d: %s", key, resp.StatusCode, string(respBody))
	}
	var issue jiraIssue
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return jiraIssue{}, fmt.Errorf("jira get issue %s: decode: %w", key, err)
	}
	return issue, nil
}

func (s *JiraSource) getComments(ctx context.Context, key string) ([]Comment, error) {
	resp, err := s.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/rest/api/3/issue/%s/comment", key), nil)
	if err != nil {
		return nil, fmt.Errorf("jira get comments %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("jira get comments %s: HTTP %d: %s", key, resp.StatusCode, string(respBody))
	}
	var result jiraCommentPage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("jira get comments %s: decode: %w", key, err)
	}
	out := make([]Comment, 0, len(result.Comments))
	for _, c := range result.Comments {
		out = append(out, Comment{
			Body:   c.bodyText(),
			Author: c.Author.DisplayName,
		})
	}
	return out, nil
}

func (s *JiraSource) addComment(ctx context.Context, key, text string) error {
	// API v3 expects ADF; text nodes can't contain newlines, so split on "\n" and insert hardBreak nodes.
	lines := strings.Split(text, "\n")
	content := make([]any, 0, len(lines)*2)
	for i, line := range lines {
		if i > 0 {
			content = append(content, map[string]any{
				"type": "hardBreak",
			})
		}
		if line != "" {
			content = append(content, map[string]any{
				"type": "text",
				"text": line,
			})
		}
	}

	payload := map[string]any{
		"body": map[string]any{
			"type":    "doc",
			"version": 1,
			"content": []any{
				map[string]any{
					"type":    "paragraph",
					"content": content,
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := s.doRequest(ctx, http.MethodPost,
		fmt.Sprintf("/rest/api/3/issue/%s/comment", key), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("jira add comment %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jira add comment %s: HTTP %d: %s", key, resp.StatusCode, string(respBody))
	}
	return nil
}

func (s *JiraSource) transitionTo(ctx context.Context, key, targetStatus string) error {
	// Transitions are workflow-dependent: list available transitions, match one whose "to" status is targetStatus, then execute it.
	resp, err := s.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/rest/api/3/issue/%s/transitions", key), nil)
	if err != nil {
		return fmt.Errorf("jira list transitions %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jira list transitions %s: HTTP %d: %s", key, resp.StatusCode, string(respBody))
	}
	var result struct {
		Transitions []jiraTransition `json:"transitions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("jira list transitions %s: decode: %w", key, err)
	}

	targetLower := strings.ToLower(strings.TrimSpace(targetStatus))
	var transitionID string
	for _, t := range result.Transitions {
		if strings.ToLower(strings.TrimSpace(t.To.Name)) == targetLower {
			transitionID = t.ID
			break
		}
	}
	if transitionID == "" {
		return fmt.Errorf("jira transition %s: no transition to status %q available (have: %s)",
			key, targetStatus, formatTransitionNames(result.Transitions))
	}

	payload := map[string]any{
		"transition": map[string]string{"id": transitionID},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp2, err := s.doRequest(ctx, http.MethodPost,
		fmt.Sprintf("/rest/api/3/issue/%s/transitions", key), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("jira execute transition %s: %w", key, err)
	}
	defer resp2.Body.Close()
	// 204 No Content on success.
	if resp2.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("jira execute transition %s: HTTP %d: %s", key, resp2.StatusCode, string(respBody))
	}
	return nil
}

func (s *JiraSource) removeLabel(ctx context.Context, key, label string) error {
	payload := map[string]any{
		"update": map[string]any{
			"labels": []map[string]string{
				{"remove": label},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := s.doRequest(ctx, http.MethodPut,
		fmt.Sprintf("/rest/api/3/issue/%s", key), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("jira remove label %s: %w", key, err)
	}
	defer resp.Body.Close()
	// 204 No Content on success.
	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jira remove label %s: HTTP %d: %s", key, resp.StatusCode, string(respBody))
	}
	return nil
}

type jiraTransition struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	To   jiraStatus `json:"to"`
}

func formatTransitionNames(transitions []jiraTransition) string {
	names := make([]string, 0, len(transitions))
	for _, t := range transitions {
		names = append(names, fmt.Sprintf("%q", t.To.Name))
	}
	return strings.Join(names, ", ")
}
