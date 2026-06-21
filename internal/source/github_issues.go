package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	ghwrap "github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
)

// GitHubIssuesConfig contains the settings for the GitHub Issues source.
type GitHubIssuesConfig struct {
	Repos        []string
	TriggerLabel string
}

// GitHubIssuesSource polls GitHub Issues by label using the gh CLI.
type GitHubIssuesSource struct {
	cfg GitHubIssuesConfig
}

func NewGitHubIssues(cfg GitHubIssuesConfig) *GitHubIssuesSource {
	return &GitHubIssuesSource{cfg: cfg}
}

func (s *GitHubIssuesSource) Name() string { return "github" }

func (s *GitHubIssuesSource) Prepare(context.Context) error {
	if len(s.cfg.Repos) == 0 {
		return fmt.Errorf("GITHUB_ISSUES_REPOS is required when github is an active ticket source")
	}
	if strings.TrimSpace(s.cfg.TriggerLabel) == "" {
		return fmt.Errorf("TRIGGER_LABEL or GITHUB_TRIGGER_LABEL is required when github is an active ticket source")
	}
	for _, raw := range s.cfg.Repos {
		if _, err := ghwrap.ExtractOwnerRepo(raw); err != nil {
			return fmt.Errorf("github issues repo %q: %w", raw, err)
		}
	}
	return nil
}

func (s *GitHubIssuesSource) Fetch(ctx context.Context) ([]Ticket, error) {
	var out []Ticket
	for _, raw := range s.cfg.Repos {
		ownerRepo, err := ghwrap.ExtractOwnerRepo(raw)
		if err != nil {
			return nil, err
		}
		issues, err := s.listIssues(ctx, ownerRepo)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			out = append(out, s.ticket(ownerRepo, issue))
		}
	}
	return out, nil
}

func (s *GitHubIssuesSource) FetchByIdentifier(ctx context.Context, identifier string) (Ticket, error) {
	for _, raw := range s.cfg.Repos {
		ownerRepo, err := ghwrap.ExtractOwnerRepo(raw)
		if err != nil {
			continue
		}
		prefix := "GH-" + strings.ToUpper(repo.Slug(ownerRepo)) + "-"
		if !strings.HasPrefix(identifier, prefix) {
			continue
		}
		num, err := strconv.Atoi(strings.TrimPrefix(identifier, prefix))
		if err != nil {
			continue
		}
		issue, err := s.viewIssue(ctx, ownerRepo, num)
		if err != nil {
			return Ticket{}, err
		}
		ticket := s.ticket(ownerRepo, issue)
		ticket.Comments = commentsFromGitHub(issue.Comments)
		return ticket, nil
	}
	return Ticket{}, fmt.Errorf("github issue %s not found in configured repos", identifier)
}

func (s *GitHubIssuesSource) FetchComments(ctx context.Context, ticket Ticket) ([]Comment, error) {
	meta, err := githubTicketMeta(ticket)
	if err != nil {
		return nil, err
	}
	issue, err := s.viewIssue(ctx, meta.OwnerRepo, meta.Number)
	if err != nil {
		return nil, err
	}
	return commentsFromGitHub(issue.Comments), nil
}

func (s *GitHubIssuesSource) RemovePlanLabel(context.Context, Ticket) error {
	return nil
}

func (s *GitHubIssuesSource) BackToTrigger(ctx context.Context, ticket Ticket, body string) error {
	return s.Comment(ctx, ticket, body)
}

func (s *GitHubIssuesSource) MarkReady(ctx context.Context, ticket Ticket, info ReadyInfo) error {
	meta, err := githubTicketMeta(ticket)
	if err != nil {
		return err
	}
	firstErr := runGH(ctx, "issue", "edit", strconv.Itoa(meta.Number),
		"--repo", meta.OwnerRepo,
		"--remove-label", s.cfg.TriggerLabel)
	body := fmt.Sprintf(
		"🌙 **Noctra created a PR** (via %s)\n\n**PR:** %s\n\nRemoved trigger label `%s`. Ready for your review!",
		info.BackendLabel, info.PRURL, s.cfg.TriggerLabel)
	if err := s.Comment(ctx, ticket, body); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *GitHubIssuesSource) Comment(ctx context.Context, ticket Ticket, body string) error {
	meta, err := githubTicketMeta(ticket)
	if err != nil {
		return err
	}
	return runGH(ctx, "issue", "comment", strconv.Itoa(meta.Number),
		"--repo", meta.OwnerRepo,
		"--body", body)
}

func (s *GitHubIssuesSource) listIssues(ctx context.Context, ownerRepo string) ([]githubIssue, error) {
	stdout, err := ghOutput(ctx, "issue", "list",
		"--repo", ownerRepo,
		"--state", "open",
		"--label", s.cfg.TriggerLabel,
		"--limit", "100",
		"--json", "number,title,body,url,labels,comments")
	if err != nil {
		return nil, err
	}
	var issues []githubIssue
	if err := json.Unmarshal(stdout, &issues); err != nil {
		return nil, fmt.Errorf("decode gh issue list for %s: %w", ownerRepo, err)
	}
	return issues, nil
}

func (s *GitHubIssuesSource) viewIssue(ctx context.Context, ownerRepo string, number int) (githubIssue, error) {
	stdout, err := ghOutput(ctx, "issue", "view", strconv.Itoa(number),
		"--repo", ownerRepo,
		"--json", "number,title,body,url,labels,comments")
	if err != nil {
		return githubIssue{}, err
	}
	var issue githubIssue
	if err := json.Unmarshal(stdout, &issue); err != nil {
		return githubIssue{}, fmt.Errorf("decode gh issue view for %s#%d: %w", ownerRepo, number, err)
	}
	return issue, nil
}

func (s *GitHubIssuesSource) ticket(ownerRepo string, issue githubIssue) Ticket {
	repoRef, repoBranch := ParseRepoDirective(issue.Body)
	if repoRef == "" {
		repoRef = ownerRepo
	}
	labels := make([]Label, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labels = append(labels, Label(l))
	}
	return Ticket{
		Source:      "github",
		ID:          fmt.Sprintf("%s#%d", ownerRepo, issue.Number),
		Identifier:  githubIdentifier(ownerRepo, issue.Number),
		Title:       issue.Title,
		Description: issue.Body,
		URL:         issue.URL,
		ProjectName: ownerRepo,
		RepoRef:     repoRef,
		RepoBranch:  repoBranch,
		Comments:    commentsFromGitHub(issue.Comments),
		Labels:      labels,
		SourceData: githubMeta{
			OwnerRepo: ownerRepo,
			Number:    issue.Number,
		},
	}
}

type githubIssue struct {
	Number   int             `json:"number"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	URL      string          `json:"url"`
	Labels   []githubLabel   `json:"labels"`
	Comments []githubComment `json:"comments"`
}

type githubLabel struct {
	Name string `json:"name"`
}

type githubComment struct {
	Body   string      `json:"body"`
	Author githubActor `json:"author"`
}

type githubActor struct {
	Login string `json:"login"`
}

type githubMeta struct {
	OwnerRepo string
	Number    int
}

func githubTicketMeta(ticket Ticket) (githubMeta, error) {
	if meta, ok := ticket.SourceData.(githubMeta); ok {
		return meta, nil
	}
	ownerRepo, numberText, ok := strings.Cut(ticket.ID, "#")
	if !ok {
		return githubMeta{}, fmt.Errorf("github ticket %s missing owner/repo#number id", ticket.Identifier)
	}
	number, err := strconv.Atoi(numberText)
	if err != nil {
		return githubMeta{}, fmt.Errorf("github ticket %s has invalid number: %w", ticket.Identifier, err)
	}
	return githubMeta{OwnerRepo: ownerRepo, Number: number}, nil
}

func githubIdentifier(ownerRepo string, number int) string {
	return fmt.Sprintf("GH-%s-%d", strings.ToUpper(repo.Slug(ownerRepo)), number)
}

func commentsFromGitHub(comments []githubComment) []Comment {
	out := make([]Comment, 0, len(comments))
	for _, c := range comments {
		out = append(out, Comment{Body: c.Body, Author: c.Author.Login})
	}
	return out
}

func ghOutput(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout, nil
}

func runGH(ctx context.Context, args ...string) error {
	_, err := ghOutput(ctx, args...)
	return err
}
