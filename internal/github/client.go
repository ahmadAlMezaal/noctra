package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"
)

// Client is a `gh`-CLI wrapper. Stateless — safe to use concurrently.
type Client struct{}

// New returns a ready-to-use Client.
func New() *Client { return &Client{} }

// ListNightshiftPRs returns every open PR that Nightshift created across the
// given repositories (any branch matching the `nightshift/` prefix authored
// by the current `gh` user).
//
// repoURLs are the git URLs from repos.json (or REPO_PATH). Each is reduced
// to `owner/name` before being passed to `gh`. Per-repo errors are logged
// (not returned) so a single unreachable repo doesn't kill the whole sweep.
func (c *Client) ListNightshiftPRs(ctx context.Context, repoURLs []string) ([]PR, error) {
	var out []PR
	for _, raw := range repoURLs {
		ownerRepo, err := ExtractOwnerRepo(raw)
		if err != nil {
			slog.Warn("github: skipping repo (cannot extract owner/name)", "url", raw, "err", err)
			continue
		}

		var stderr strings.Builder
		cmd := exec.CommandContext(ctx, "gh", "pr", "list",
			"--repo", ownerRepo,
			"--author", "@me",
			"--state", "open",
			"--json", "url,number,title,headRefName",
		)
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		if err != nil {
			slog.Warn("github: gh pr list failed", "repo", ownerRepo, "err", err, "stderr", strings.TrimSpace(stderr.String()))
			continue
		}

		var prs []PR
		if err := json.Unmarshal(stdout, &prs); err != nil {
			slog.Warn("github: decode pr list output", "repo", ownerRepo, "err", err)
			continue
		}

		for _, pr := range prs {
			if strings.HasPrefix(pr.HeadRefName, "nightshift/") {
				out = append(out, pr)
			}
		}
	}
	return out, nil
}

// GetPR fetches the full view of a PR — comments, reviews, state — used by
// the watcher to diff against the cursor in state.json.
func (c *Client) GetPR(ctx context.Context, prURL string) (*Details, error) {
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", prURL,
		"--json", "url,number,state,comments,reviews",
	)
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view %s: %w (%s)", prURL, err, strings.TrimSpace(stderr.String()))
	}
	var d Details
	if err := json.Unmarshal(stdout, &d); err != nil {
		return nil, fmt.Errorf("decode gh pr view %s: %w", prURL, err)
	}

	// Inline review-thread comments aren't returned by `gh pr view`; pull
	// them from the REST API. Non-fatal: on failure we degrade to
	// conversation comments + review summaries rather than skipping the PR.
	if rc, err := c.listReviewComments(ctx, prURL); err != nil {
		slog.Warn("github: fetch inline review comments failed", "url", prURL, "err", err)
	} else {
		d.ReviewComments = rc
	}
	return &d, nil
}

// listReviewComments fetches the inline review-thread comments for a PR via
// the REST API (paginated — bots can leave many on one review).
func (c *Client) listReviewComments(ctx context.Context, prURL string) ([]ReviewComment, error) {
	apiPath, err := reviewCommentsAPIPath(prURL)
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "gh", "api", "--paginate", apiPath)
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w (%s)", apiPath, err, strings.TrimSpace(stderr.String()))
	}
	comments, err := decodeReviewComments(stdout)
	if err != nil {
		return nil, fmt.Errorf("decode review comments %s: %w", apiPath, err)
	}
	return comments, nil
}

// decodeReviewComments parses the output of `gh api --paginate`. Depending on
// the gh version this is either a single merged JSON array or several arrays
// concatenated (one per page); a streaming decoder handles both.
func decodeReviewComments(stdout []byte) ([]ReviewComment, error) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	var comments []ReviewComment
	for dec.More() {
		var page []ReviewComment
		if err := dec.Decode(&page); err != nil {
			return nil, err
		}
		comments = append(comments, page...)
	}
	return comments, nil
}

// reviewCommentsAPIPath turns a PR URL into the REST path for its inline
// review comments: https://github.com/owner/name/pull/12 →
// repos/owner/name/pulls/12/comments.
func reviewCommentsAPIPath(prURL string) (string, error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return "", fmt.Errorf("parse PR URL %q: %w", prURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", fmt.Errorf("unexpected PR URL shape: %q", prURL)
	}
	return fmt.Sprintf("repos/%s/%s/pulls/%s/comments", parts[0], parts[1], parts[3]), nil
}

// ExtractOwnerRepo reduces a git remote URL (SSH or HTTPS) to its `owner/name`
// form, which is what `gh --repo` wants. An already-reduced "owner/name" is
// returned as-is.
//
//	git@github.com:me/auth.git   → me/auth
//	https://github.com/me/auth   → me/auth
//	me/auth                      → me/auth
func ExtractOwnerRepo(raw string) (string, error) {
	s := strings.TrimSuffix(strings.TrimSpace(raw), ".git")

	if strings.HasPrefix(s, "git@") {
		idx := strings.Index(s, ":")
		if idx < 0 {
			return "", fmt.Errorf("ssh URL missing ':' in %q", raw)
		}
		rest := s[idx+1:]
		if !looksLikeOwnerRepo(rest) {
			return "", fmt.Errorf("unexpected ssh URL shape: %q", raw)
		}
		return rest, nil
	}

	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("parse URL %q: %w", raw, err)
		}
		rest := strings.Trim(u.Path, "/")
		if !looksLikeOwnerRepo(rest) {
			return "", fmt.Errorf("URL path %q is not owner/name", u.Path)
		}
		return rest, nil
	}

	if looksLikeOwnerRepo(s) {
		return s, nil
	}
	return "", fmt.Errorf("cannot extract owner/name from %q", raw)
}

func looksLikeOwnerRepo(s string) bool {
	return strings.Count(s, "/") == 1 && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/")
}
