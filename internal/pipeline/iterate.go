package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/agent"
	"github.com/ahmadAlMezaal/nightshift/internal/github"
	"github.com/ahmadAlMezaal/nightshift/internal/repo"
	"github.com/ahmadAlMezaal/nightshift/internal/state"
	"github.com/ahmadAlMezaal/nightshift/internal/watch"
)

// runWatcher is the PR-poll loop counterpart to the main Linear-poll loop.
// Started by Run when cfg.AutoIteratePRs is true and the watcher initialised
// without error.
func (p *Pipeline) runWatcher(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	slog.Info("pr watcher starting",
		"interval", p.cfg.PRPollInterval,
		"max_iterations", p.cfg.MaxPRIterations,
		"trusted_reviewers", p.cfg.TrustedReviewers,
	)

	ticker := time.NewTicker(p.cfg.PRPollInterval)
	defer ticker.Stop()

	// Initial scan after a brief delay so the Linear-startup output clears.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	p.prPollOnce(ctx, wg)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.prPollOnce(ctx, wg)
		}
	}
}

// prPollOnce is one watcher tick: scan PRs, advance cursors for non-actionable
// events, and dispatch iteratePR goroutines for everything that is actionable
// (and not at the iteration cap).
func (p *Pipeline) prPollOnce(ctx context.Context, wg *sync.WaitGroup) {
	scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	changes, err := p.watcher.Scan(scanCtx)
	cancel()
	if err != nil {
		slog.Warn("pr poll: scan failed", "err", err)
		return
	}

	slog.Info("pr poll", "prs_with_changes", len(changes))

	for _, ch := range changes {
		// Even with no actionable events (all-APPROVED / all-skipped),
		// advance the cursor so we don't re-evaluate the same events on
		// every poll.
		if len(ch.Events) == 0 {
			p.advanceCursor(ch)
			continue
		}

		identifier := identifierFromBranch(ch.PR.HeadRefName)
		if identifier == "" {
			slog.Warn("pr poll: branch is not a Nightshift branch; skipping",
				"branch", ch.PR.HeadRefName)
			continue
		}

		cursor := p.store.Get(ch.PR.URL)
		if cursor.Iterations >= p.cfg.MaxPRIterations {
			slog.Info("pr at iteration cap — skipping",
				"pr", ch.PR.URL, "iterations", cursor.Iterations, "cap", p.cfg.MaxPRIterations)
			// Advance cursor anyway so the same skipped events don't
			// keep getting "discovered" forever.
			p.advanceCursor(ch)
			continue
		}

		p.mu.Lock()
		if _, dupe := p.active[identifier]; dupe {
			p.mu.Unlock()
			slog.Info("pr iteration skipped — ticket already in progress", "id", identifier)
			continue
		}
		if len(p.active) >= p.cfg.MaxConcurrent {
			p.mu.Unlock()
			slog.Info("pr iteration deferred — at capacity", "id", identifier)
			// continue, not return: this PR waits for the next tick, but
			// later non-actionable PRs in this batch still need their cursors
			// advanced.
			continue
		}
		p.active[identifier] = struct{}{}
		p.mu.Unlock()

		wg.Add(1)
		go func(ch watch.PRChanges, id string) {
			defer wg.Done()
			defer p.markDone(id)
			p.iteratePR(ctx, ch, id)
		}(ch, identifier)
	}
}

// iteratePR is one re-engagement on an open PR. Resolves the repo, resumes
// the existing remote branch, builds a fix prompt from the new feedback,
// runs Claude, and pushes the follow-up commit.
func (p *Pipeline) iteratePR(ctx context.Context, ch watch.PRChanges, identifier string) {
	logger := slog.With("id", identifier, "pr", ch.PR.URL)
	logger.Info("re-engaging on PR feedback", "events", len(ch.Events))

	// Heads-up Telegram (always — not gated by TELEGRAM_VERBOSE).
	p.telegram.Send(ctx, fmt.Sprintf("🔄 *%s* — addressing review on PR #%d", identifier, ch.PR.Number))

	// On every failure path below we record the iteration before returning.
	// Otherwise the cursor never advances and the next poll re-discovers the
	// same feedback, re-runs, and fails again — an infinite retry loop. The
	// only exceptions are infra failures (timeout / rate-limit), handled
	// further down, which intentionally retry.
	project, err := p.matchPRtoProject(ch.PR.URL)
	if err != nil {
		logger.Error("could not match PR to a registered project", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, "")
		return
	}

	resolved, err := p.resolver.Resolve(ctx, project)
	if err != nil {
		logger.Error("repo resolve failed", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, "")
		return
	}

	wt, err := repo.ResumeWorktree(ctx, p.cfg.WorktreeBase, identifier, resolved.Path)
	if err != nil {
		logger.Error("resume worktree failed", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, "")
		return
	}
	defer repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, identifier)

	// Fetch ticket context from Linear so the fix prompt has Title + Description.
	// If Linear is unreachable, fall back to placeholders rather than aborting.
	var title, description, issueID string
	if issue, err := p.linear.GetIssueByIdentifier(ctx, identifier); err == nil {
		title = issue.Title
		description = issue.Description
		issueID = issue.ID
	} else {
		logger.Warn("could not fetch Linear ticket — proceeding without context", "err", err)
		title = identifier
	}

	items := make([]agent.FeedbackItem, 0, len(ch.Events))
	for _, ev := range ch.Events {
		items = append(items, agent.FeedbackItem{
			Kind:   string(ev.Type),
			Author: ev.Author.Login,
			Body:   ev.Body,
			URL:    ev.URL,
			State:  ev.ReviewState,
			Path:   ev.Path,
			Line:   ev.Line,
		})
	}

	prompt := agent.BuildFixPrompt(agent.FixPromptInput{
		Identifier:  identifier,
		Title:       title,
		Description: description,
		PRNumber:    ch.PR.Number,
		PRURL:       ch.PR.URL,
		Feedback:    items,
	})

	logFile := filepath.Join(p.cfg.LogDir, identifier+".log")
	_ = agent.AttemptHeader(logFile)
	offset := agent.OffsetBefore(logFile)

	runErr := agent.Run(ctx, agent.RunOptions{
		Workdir:       wt.Path,
		Prompt:        prompt,
		LogFile:       logFile,
		Timeout:       p.cfg.AgentTimeout,
		UseAgentTeams: p.cfg.UseAgentTeams,
	})

	// Don't increment iteration count on infrastructure-level failures
	// (timeout / rate-limit) — those weren't really attempts on the feedback.
	if errors.Is(runErr, agent.ErrTimedOut) {
		logger.Warn("iteration timed out — will retry next poll", "timeout", p.cfg.AgentTimeout)
		return
	}

	output := agent.ReadAfter(logFile, offset)
	if agent.HasRateLimit(output) {
		logger.Warn("rate limit detected during iteration")
		p.flagRateLimit()
		return
	}
	if runErr != nil {
		logger.Error("claude run failed", "err", runErr)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		return
	}

	if blocked := agent.BlockedLine(output); blocked != "" {
		logger.Info("claude blocked on review feedback", "line", blocked)
		if issueID != "" {
			_ = p.linear.Comment(ctx, issueID, fmt.Sprintf(
				"🚧 **Nightshift: blocked on PR feedback**\n\n> %s\n\nLeft the PR as-is. Reply to the PR with clarification, then move the ticket to **%s** to retry.",
				blocked, p.cfg.TriggerState))
		}
		// Advance cursor + count attempt so we don't loop on the same feedback.
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		return
	}

	// Apply changes (if any).
	hasChanges, err := workingTreeChanged(ctx, wt.Path)
	if err != nil {
		logger.Error("git status failed", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		return
	}
	if hasChanges {
		if err := runIn(ctx, wt.Path, "git", "add", "-A"); err != nil {
			logger.Error("git add failed", "err", err)
			p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
			return
		}
		commitMsg := fmt.Sprintf("fix: address review feedback on %s\n\nFollow-up commit by Nightshift addressing %d new feedback item(s).",
			identifier, len(ch.Events))
		if err := runIn(ctx, wt.Path, "git", "commit", "-m", commitMsg); err != nil {
			logger.Error("git commit failed", "err", err)
			p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
			return
		}
		if err := runIn(ctx, wt.Path, "git", "push", "origin", wt.Branch); err != nil {
			logger.Error("git push failed", "err", err)
			p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
			return
		}
		logger.Info("pushed follow-up commit", "branch", wt.Branch)
	} else {
		logger.Info("iteration produced no diff — feedback addressed without code edits, or Claude chose not to act")
	}

	p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
}

// recordIteration bumps the per-PR iteration counter, advances the comment +
// review cursors, and fires the cap-hit notifications on the transition.
func (p *Pipeline) recordIteration(ctx context.Context, ch watch.PRChanges, identifier string, prNumber int, issueID string) {
	var iterations int
	if err := p.store.Update(ch.PR.URL, func(r *state.PRState) {
		if r.TicketID == "" {
			r.TicketID = identifier
		}
		if ch.NewestComment.After(r.LastCommentAt) {
			r.LastCommentAt = ch.NewestComment
		}
		if ch.NewestReview.After(r.LastReviewAt) {
			r.LastReviewAt = ch.NewestReview
		}
		r.Iterations++
		r.LastIteratedAt = time.Now()
		iterations = r.Iterations
	}); err != nil {
		slog.Warn("pipeline: state update failed", "url", ch.PR.URL, "err", err)
	}

	if iterations >= p.cfg.MaxPRIterations {
		p.telegram.Send(ctx, fmt.Sprintf(
			"🛑 *%s* — PR #%d hit iteration cap (%d attempts). Needs human attention.",
			identifier, prNumber, iterations))
		if issueID != "" {
			_ = p.linear.Comment(ctx, issueID, fmt.Sprintf(
				"🛑 **Nightshift: PR iteration cap reached** (%d attempts on PR %s).\n\nNeeds a human to take a look — Nightshift won't re-engage on this PR again unless you reset the iteration count in `~/.nightshift-state.json` or close the PR.",
				iterations, ch.PR.URL))
		}
	}
}

// advanceCursor moves the per-PR comment/review cursor forward without
// incrementing the iteration counter — used when a poll finds only non-
// actionable events (APPROVED reviews, skipped bot comments, etc.).
func (p *Pipeline) advanceCursor(ch watch.PRChanges) {
	if err := p.store.Update(ch.PR.URL, func(r *state.PRState) {
		if ch.NewestComment.After(r.LastCommentAt) {
			r.LastCommentAt = ch.NewestComment
		}
		if ch.NewestReview.After(r.LastReviewAt) {
			r.LastReviewAt = ch.NewestReview
		}
	}); err != nil {
		slog.Warn("pipeline: cursor advance failed", "url", ch.PR.URL, "err", err)
	}
}

// matchPRtoProject finds the Linear project name in repos.json whose
// configured URL points at the same owner/repo as the PR.
func (p *Pipeline) matchPRtoProject(prURL string) (string, error) {
	target, err := prRepoOwnerRepo(prURL)
	if err != nil {
		return "", err
	}
	if p.cfg.Registry == nil {
		return "", fmt.Errorf("no registry configured; cannot match PR %s to a project", prURL)
	}
	for name, entry := range p.cfg.Registry.Repos {
		if got, err := github.ExtractOwnerRepo(entry.URL); err == nil && got == target {
			return name, nil
		}
	}
	return "", fmt.Errorf("no registry entry matches %s (looking for %s)", prURL, target)
}

// prRepoOwnerRepo extracts owner/name from a PR URL like
// https://github.com/owner/name/pull/N.
func prRepoOwnerRepo(prURL string) (string, error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return "", fmt.Errorf("parse PR URL %q: %w", prURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("PR URL path too short: %q", u.Path)
	}
	return parts[0] + "/" + parts[1], nil
}

// identifierFromBranch turns "nightshift/eng-42" into "ENG-42". Returns ""
// if the branch isn't a Nightshift branch.
func identifierFromBranch(branch string) string {
	if !strings.HasPrefix(branch, "nightshift/") {
		return ""
	}
	return strings.ToUpper(strings.TrimPrefix(branch, "nightshift/"))
}
