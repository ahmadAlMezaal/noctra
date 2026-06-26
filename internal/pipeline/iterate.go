package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/lessons"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/state"
	"github.com/ahmadAlMezaal/noctra/internal/watch"
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
	lessons.ProcessMergedPRs(ctx, p.store, p.gh, p.resolver, p.review)

	scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	changes, err := p.watcher.Scan(scanCtx)
	cancel()
	if err != nil {
		slog.Warn("pr poll: scan failed", "err", err)
		return
	}

	slog.Info("pr poll", "prs_with_changes", len(changes))

	for _, ch := range changes {
		identifier := identifierFromBranch(ch.PR.HeadRefName)
		newComments, newReviews := countEvents(ch.Events)
		ciFailed := ch.CIFailure != nil

		// Even with no actionable events (all-APPROVED / all-skipped) and no
		// CI failure, advance the cursor so we don't re-evaluate the same
		// events on every poll.
		if len(ch.Events) == 0 && !ciFailed {
			reason := "none"
			if len(ch.Skipped) > 0 {
				reason = "untrusted-bot"
			}
			slog.Info("pr poll detail",
				"pr", ch.PR.Number, "id", identifier,
				"new_comments", newComments, "new_reviews", newReviews,
				"ci_failed", ciFailed,
				"action", "skip", "reason", reason,
			)
			p.advanceCursor(ch)
			continue
		}

		if identifier == "" {
			slog.Warn("pr poll: branch is not a Noctra branch; skipping",
				"branch", ch.PR.HeadRefName)
			continue
		}

		cursor := p.store.Get(ch.PR.URL)
		if cursor.Iterations >= p.cfg.MaxPRIterations {
			slog.Info("pr poll detail",
				"pr", ch.PR.Number, "id", identifier,
				"new_comments", newComments, "new_reviews", newReviews,
				"ci_failed", ciFailed,
				"action", "skip", "reason", "cap",
			)
			// Advance cursor anyway so the same skipped events don't
			// keep getting "discovered" forever.
			p.advanceCursor(ch)
			continue
		}

		p.mu.Lock()
		if _, dupe := p.active[identifier]; dupe {
			p.mu.Unlock()
			slog.Info("pr poll detail",
				"pr", ch.PR.Number, "id", identifier,
				"new_comments", newComments, "new_reviews", newReviews,
				"ci_failed", ciFailed,
				"action", "skip", "reason", "in-progress",
			)
			continue
		}
		if len(p.active) >= p.cfg.MaxConcurrent {
			p.mu.Unlock()
			slog.Info("pr poll detail",
				"pr", ch.PR.Number, "id", identifier,
				"new_comments", newComments, "new_reviews", newReviews,
				"ci_failed", ciFailed,
				"action", "skip", "reason", "at-capacity",
			)
			// continue, not return: this PR waits for the next tick, but
			// later non-actionable PRs in this batch still need their cursors
			// advanced.
			continue
		}
		ticketCtx, ticketCancel := context.WithCancel(ctx)
		p.active[identifier] = struct{}{}
		p.cancels[identifier] = ticketCancel
		p.mu.Unlock()
		p.publishDashboardChange()

		slog.Info("pr poll detail",
			"pr", ch.PR.Number, "id", identifier,
			"new_comments", newComments, "new_reviews", newReviews,
			"ci_failed", ciFailed,
			"action", "iterate",
		)

		wg.Add(1)
		go func(ch watch.PRChanges, id string) {
			defer wg.Done()
			defer p.markDone(id)
			p.iteratePR(ticketCtx, ch, id)
		}(ch, identifier)
	}
}

// countEvents tallies actionable events by type for structured logging.
func countEvents(events []watch.Event) (comments, reviews int) {
	for _, ev := range events {
		switch ev.Type {
		case watch.EventComment:
			comments++
		case watch.EventReview:
			reviews++
		}
	}
	return
}

// resolveIterateBackend picks the coding-agent backend for a PR iteration.
// Priority: persisted state → issue's current labels → pipeline default.
func (p *Pipeline) resolveIterateBackend(ctx context.Context, prURL, identifier string) agent.Backend {
	// 1. Persisted state — the backend the PR was created with.
	if p.store != nil {
		if cursor := p.store.Get(prURL); cursor.AgentBackend != "" {
			if b, err := agent.New(cursor.AgentBackend); err == nil {
				return b
			}
			slog.Warn("persisted backend invalid — trying labels",
				"id", identifier, "backend", cursor.AgentBackend)
		}
	}

	// 2. Re-derive from the issue's current labels.
	if issue, err := p.linear.GetIssueByIdentifier(ctx, identifier); err == nil {
		if label := issue.BackendLabel(); label != "" {
			if b, err := agent.New(label); err == nil {
				return b
			}
		}
	}

	// 3. Pipeline default.
	return p.agent
}

// iteratePR is one re-engagement on an open PR. Resolves the repo, resumes
// the existing remote branch, builds a fix prompt from the new feedback,
// runs Claude, and pushes the follow-up commit.
func (p *Pipeline) iteratePR(ctx context.Context, ch watch.PRChanges, identifier string) {
	startedAt := time.Now()
	logger := slog.With("id", identifier, "pr", ch.PR.URL)
	logger.Info("re-engaging on PR", "events", len(ch.Events), "ci_failed", ch.CIFailure != nil)

	p.ackEngagement(ctx, ch) // 👀 ack before the agent's slow reply

	// Select the backend for this iteration — persisted state (from when the
	// PR was created) takes priority so follow-up commits use the same backend.
	backend := p.resolveIterateBackend(ctx, ch.PR.URL, identifier)

	// Heads-up Telegram (always — not gated by VERBOSE_NOTIFICATIONS).
	p.notifier.Send(ctx, fmt.Sprintf("🔄 *%s* — %s on PR #%d", notify.EscapeMarkdown(identifier), engagementSummary(ch), ch.PR.Number))

	// On every failure path below we record the iteration before returning.
	// Otherwise the cursor never advances and the next poll re-discovers the
	// same feedback, re-runs, and fails again — an infinite retry loop. The
	// only exceptions are infra failures (timeout / rate-limit), handled
	// further down, which intentionally retry.
	// Resolve the repo this PR lives in. Prefer the remote URL of the clone the
	// watcher discovered it through (ch.PR.RepoURL) — that preserves the clone's
	// transport (e.g. an SSH `git@…` URL for a private repo). Only fall back to
	// the PR's bare owner/name when that's unavailable; ResolveDirect would then
	// synthesize an HTTPS URL, which fails on SSH-only hosts.
	ref := ch.PR.RepoURL
	if ref == "" {
		var err error
		ref, err = prRepoOwnerRepo(ch.PR.URL)
		if err != nil {
			logger.Error("could not parse PR repo from URL", "err", err)
			p.recordIteration(ctx, ch, identifier, ch.PR.Number, "")
			p.recordRun(state.RunHistory{
				Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
				AgentBackend: backend.Name(), RunType: "iterate",
				StartedAt: startedAt, FinishedAt: time.Now(), Status: "failed",
			})
			return
		}
	}
	resolved, err := p.resolver.ResolveDirect(ctx, ref, "")
	if err != nil {
		logger.Error("repo resolve (direct) failed", "err", err, "ref", ref)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, "")
		p.recordRun(state.RunHistory{
			Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
			AgentBackend: backend.Name(), RunType: "iterate",
			StartedAt: startedAt, FinishedAt: time.Now(), Status: "failed",
		})
		return
	}

	p.mu.Lock()
	p.activeRepos[identifier] = filepath.Base(resolved.Path)
	p.mu.Unlock()
	p.publishDashboardChange()

	wt, err := repo.ResumeWorktree(ctx, p.cfg.WorktreeBase, identifier, resolved.Path)
	if err != nil {
		logger.Error("resume worktree failed", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, "")
		p.recordRun(state.RunHistory{
			Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
			Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
			RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
			Status: "failed",
		})
		return
	}
	logger.Info("resume worktree", "path", wt.Path)
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

	// Fetch failed-step logs for any failing CI checks (best-effort — if the
	// fetch fails, Claude can still reproduce the failure locally).
	var ciItems []agent.CIItem
	if ch.CIFailure != nil {
		for _, chk := range ch.CIFailure.FailedChecks {
			item := agent.CIItem{Name: chk.CheckName(), URL: chk.URL()}
			if logs, err := p.gh.CheckLogs(ctx, chk); err == nil {
				item.Logs = logs
			} else if errors.Is(err, github.ErrNotActionsRun) {
				// Expected for non-Actions checks (CircleCI, Vercel, …) — no
				// logs to fetch; Claude reproduces locally. Not worth a warning.
				logger.Debug("skipping log fetch for non-Actions check", "check", chk.CheckName())
			} else {
				logger.Warn("could not fetch CI logs — Claude will reproduce locally", "check", chk.CheckName(), "err", err)
			}
			ciItems = append(ciItems, item)
		}
	}

	var repoLessons string
	if p.store != nil {
		repoSlug := repo.Slug(filepath.Base(resolved.Path))
		if l, err := p.store.GetLessons(repoSlug); err == nil {
			repoLessons = l
		}
	}

	var priorReasoning string
	if p.store != nil {
		priorReasoning = p.store.Get(ch.PR.URL).LastReasoning
	}

	prompt := agent.BuildFixPrompt(agent.FixPromptInput{
		Identifier:     identifier,
		Title:          title,
		Description:    description,
		PRNumber:       ch.PR.Number,
		PRURL:          ch.PR.URL,
		Feedback:       items,
		CI:             ciItems,
		RepoLessons:    repoLessons,
		PriorReasoning: priorReasoning,
	})

	logFile := filepath.Join(p.cfg.LogDir, identifier+".log")
	_ = agent.AttemptHeader(logFile)
	offset := agent.OffsetBefore(logFile)

	logger.Info("running agent", "backend", backend.Name(), "log", logFile)

	headBefore := gitHead(ctx, wt.Path)

	runErr := backend.Run(ctx, agent.RunOptions{
		Workdir:       wt.Path,
		Prompt:        prompt,
		LogFile:       logFile,
		Timeout:       p.cfg.AgentTimeout,
		UseAgentTeams: p.cfg.UseAgentTeams,
	})

	// Killed via /kill — skip all error handling and let the defer clean up.
	if p.isKilled(identifier) {
		logger.Info("run killed by user")
		return
	}

	// Context cancelled (graceful shutdown) — don't treat the cancellation as a
	// real iteration: recording it would bump the PR's iteration count (and could
	// fire the cap warning) on the way down. Mirrors the same guard in process().
	if ctx.Err() != nil {
		logger.Info("iteration cancelled (shutdown)", "reason", ctx.Err())
		return
	}

	// Don't increment iteration count on infrastructure-level failures
	// (timeout / rate-limit) — those weren't really attempts on the feedback.
	if errors.Is(runErr, agent.ErrTimedOut) {
		logger.Warn("iteration timed out — will retry next poll", "timeout", p.cfg.AgentTimeout)
		return
	}

	output := agent.ReadAfter(logFile, offset)

	// Record usage from the iteration (ENG-217).
	usage := agent.ParseUsage(output)
	p.budget.Record(usage.TotalTokens, usage.CostUSD)
	p.recordUsage(usage, "iterate", identifier, ch.PR.URL, backend)
	// Check budget caps immediately after recording — if exceeded, the pause
	// takes effect on the next poll tick (in-flight work drains naturally).
	if reason := p.budget.ExceededReason(); reason != "" {
		p.flagBudgetExceeded(reason)
		p.notifier.Send(ctx, fmt.Sprintf(
			"⏸ *Daily budget exceeded*\n%s\nDispatching paused until next UTC midnight.",
			notify.EscapeMarkdown(reason)))
	}

	// Rate limit is only classified on a failed run (see rateLimited), so a
	// successful iteration whose transcript merely mentions "rate limit" (file
	// content / diff) doesn't trigger a false shutdown.
	if rateLimited(backend, runErr, output) {
		logger.Warn("rate limit detected during iteration")
		p.flagRateLimit()
		return
	}
	if runErr != nil {
		logger.Error("agent run failed", "err", runErr)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		p.recordRun(state.RunHistory{
			Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
			Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
			RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
			Status: "failed",
		})
		return
	}

	if blocked := agent.BlockedLine(output); blocked != "" {
		logger.Info("blocked", "reason", blocked)
		if issueID != "" {
			_ = p.linear.Comment(ctx, issueID, fmt.Sprintf(
				"🚧 **Noctra: blocked on PR feedback**\n\n> %s\n\nLeft the PR as-is. Reply to the PR with clarification, then move the ticket to **%s** to retry.",
				blocked, p.cfg.TriggerState))
		}
		// Advance cursor + count attempt so we don't loop on the same feedback.
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		p.recordRun(state.RunHistory{
			Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
			Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
			RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
			Status: "blocked",
		})
		return
	}

	summary := strings.TrimSpace(agent.ExtractSummary(output))

	// Push whatever the iteration produced. Claude may leave changes
	// uncommitted OR commit them itself — so stage + commit anything pending,
	// then push whenever the branch is ahead of its remote. Gating on a dirty
	// worktree alone silently dropped Claude's own commits (ENG-182).
	if err := runIn(ctx, wt.Path, "git", "add", "-A"); err != nil {
		logger.Error("git add failed", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		p.recordRun(state.RunHistory{
			Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
			Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
			RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
			Status: "failed",
		})
		return
	}
	staged, err := hasStagedChanges(ctx, wt.Path)
	if err != nil {
		logger.Error("git diff --cached failed", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		p.recordRun(state.RunHistory{
			Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
			Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
			RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
			Status: "failed",
		})
		return
	}
	if staged {
		commitMsg := appendCoAuthorTrailer(
			fmt.Sprintf("fix: address PR feedback on %s\n\nFollow-up commit by Noctra (%s).",
				identifier, engagementSummary(ch)),
			backend.CoAuthor())
		if err := runIn(ctx, wt.Path, "git", "commit", "-m", commitMsg); err != nil {
			logger.Error("git commit failed", "err", err)
			p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
			p.recordRun(state.RunHistory{
				Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
				Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
				RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
				Status: "failed",
			})
			return
		}
	}
	ahead, err := branchAhead(ctx, wt.Path, "origin/"+wt.Branch)
	if err != nil {
		logger.Error("git rev-list failed", "err", err)
		p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
		p.recordRun(state.RunHistory{
			Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
			Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
			RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
			Status: "failed",
		})
		return
	}
	// Key "addressed" on HEAD moving, not branch-ahead: the agent may self-push.
	headAfter := gitHead(ctx, wt.Path)
	moved := headBefore != "" && headAfter != headBefore
	if moved || ahead {
		if ahead {
			if err := runIn(ctx, wt.Path, "git", "push", "origin", wt.Branch); err != nil {
				logger.Error("git push failed", "err", err)
				p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
				p.recordRun(state.RunHistory{
					Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
					Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
					RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
					Status: "failed",
				})
				return
			}
		}
		sha := gitHeadShort(ctx, wt.Path)
		logger.Info("follow-up commit", "sha", sha, "branch", wt.Branch, "pushed_by_agent", !ahead)

		fullSHA := headAfter
		if p.store != nil && (fullSHA != "" || summary != "") {
			if err := p.store.Update(ch.PR.URL, func(r *state.PRState) {
				if fullSHA != "" {
					r.LastPushedSHA = fullSHA
				}
				r.LastReasoning = summary
			}); err != nil {
				logger.Warn("could not persist PR state", "err", err)
			}
		}

		convReply := fmt.Sprintf("Addressed in %s.", sha)
		if summary != "" {
			convReply = fmt.Sprintf("Addressed in %s.\n\n%s", sha, summary)
		}
		p.postIterationReplies(ctx, ch, output, sha, convReply, logger)

		// Completion heads-up (always — mirrors the 🔄 start ping and the
		// ✅ "PR ready" ping the main ticket flow sends on success).
		p.notifier.Send(ctx, fmt.Sprintf("✅ *%s* — pushed follow-up to PR #%d (%s)",
			notify.EscapeMarkdown(identifier), ch.PR.Number, engagementSummary(ch)))
	} else {
		logger.Info("no diff produced")
		convReply := "Noctra reviewed this but made no change — it appears already addressed or no longer applicable. Re-open if you'd like it revisited."
		if summary != "" {
			convReply = "Noctra reviewed this and made no code change:\n\n" + summary
		}
		p.postIterationReplies(ctx, ch, output, "", convReply, logger)
		if p.store != nil && summary != "" {
			if err := p.store.Update(ch.PR.URL, func(r *state.PRState) {
				r.LastReasoning = summary
			}); err != nil {
				logger.Warn("could not persist PR reasoning", "err", err)
			}
		}
		p.notifier.Send(ctx, fmt.Sprintf("✅ *%s* — reviewed PR #%d, no code changes needed",
			notify.EscapeMarkdown(identifier), ch.PR.Number))
	}

	iterateStatus := "no_change"
	if moved || ahead {
		iterateStatus = "pr_opened"
	}
	p.recordRun(state.RunHistory{
		Identifier: identifier, TicketID: identifier, PRURL: ch.PR.URL,
		Repo: filepath.Base(resolved.Path), AgentBackend: backend.Name(),
		RunType: "iterate", StartedAt: startedAt, FinishedAt: time.Now(),
		Status: iterateStatus, Iterations: 1,
	})
	p.recordIteration(ctx, ch, identifier, ch.PR.Number, issueID)
}

// recordIteration bumps the per-PR iteration counter, advances the comment +
// review cursors, and fires the cap-hit notifications on the transition.
func (p *Pipeline) recordIteration(ctx context.Context, ch watch.PRChanges, identifier string, prNumber int, issueID string) {
	var (
		iterations  int
		lastComment time.Time
		lastReview  time.Time
		lastCISHA   string
	)
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
		if ch.CIFailure != nil && ch.CIFailure.SHA != "" {
			r.LastCISHA = ch.CIFailure.SHA
			if len(ch.CIFailure.FailedChecks) > 0 {
				r.LastCIRunURL = ch.CIFailure.FailedChecks[0].URL()
			}
		}
		r.Iterations++
		r.LastIteratedAt = time.Now()
		iterations = r.Iterations
		lastComment = r.LastCommentAt
		lastReview = r.LastReviewAt
		lastCISHA = r.LastCISHA
	}); err != nil {
		slog.Warn("pipeline: state update failed", "url", ch.PR.URL, "err", err)
		return
	}

	slog.Info("cursor advanced",
		"id", identifier, "pr", prNumber,
		"last_comment", lastComment, "last_review", lastReview,
		"last_ci_sha", lastCISHA,
		"iterations", fmt.Sprintf("%d/%d", iterations, p.cfg.MaxPRIterations),
	)

	if iterations >= p.cfg.MaxPRIterations {
		p.notifier.Send(ctx, fmt.Sprintf(
			"🛑 *%s* — PR #%d hit iteration cap (%d attempts). Needs human attention.",
			notify.EscapeMarkdown(identifier), prNumber, iterations))
		if issueID != "" {
			_ = p.linear.Comment(ctx, issueID, fmt.Sprintf(
				"🛑 **Noctra: PR iteration cap reached** (%d attempts on PR %s).\n\nNeeds a human to take a look — Noctra won't re-engage on this PR again unless you reset the iteration count in the state DB or close the PR.",
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
		if ch.CIFailure != nil && ch.CIFailure.SHA != "" {
			r.LastCISHA = ch.CIFailure.SHA
		}
	}); err != nil {
		slog.Warn("pipeline: cursor advance failed", "url", ch.PR.URL, "err", err)
	}
}

// ackEngagement posts a best-effort 👀 on each comment that triggered re-engagement.
func (p *Pipeline) ackEngagement(ctx context.Context, ch watch.PRChanges) {
	for _, ev := range ch.Events {
		if ev.Type != watch.EventComment || ev.CommentID == "" {
			continue
		}
		if err := p.gh.AddEyesReaction(ctx, ch.PR.URL, ev.CommentID, ev.Path != ""); err != nil {
			slog.Warn("ack reaction failed", "pr", ch.PR.URL, "err", err)
		}
	}
}

// postIterationReplies routes the agent's per-finding statuses to their review
// threads, resolving only the ones it addressed (sha is empty on a no-diff
// iteration). With no per-finding block it falls back to one conversation
// comment, never the old broadcast to every thread.
func (p *Pipeline) postIterationReplies(ctx context.Context, ch watch.PRChanges, agentOutput, sha, convReply string, logger *slog.Logger) {
	threadReplies := map[int64]github.ThreadReply{}
	if findings, ok := agent.ExtractFindingReplies(agentOutput); ok {
		for _, f := range findings {
			idx := f.Finding - 1
			if idx < 0 || idx >= len(ch.Events) {
				continue
			}
			ev := ch.Events[idx]
			if ev.Path == "" || ev.CommentID == "" {
				continue
			}
			commentID, err := strconv.ParseInt(ev.CommentID, 10, 64)
			if err != nil {
				continue
			}
			body := f.Reply
			if f.Addressed && sha != "" {
				body = fmt.Sprintf("Addressed in %s.\n\n%s", sha, f.Reply)
			}
			threadReplies[commentID] = github.ThreadReply{Body: body, Resolve: f.Addressed}
		}
	}

	p.gh.ReplyToThreadsByComment(ctx, ch.PR.URL, threadReplies)

	if hasConversationComment(ch) {
		p.replyToConversation(ctx, ch, convReply, logger)
		return
	}
	if len(threadReplies) == 0 {
		if err := p.gh.PostComment(ctx, ch.PR.URL, convReply); err != nil {
			logger.Warn("post iteration reply failed", "err", err)
		}
	}
}

func (p *Pipeline) replyToConversation(ctx context.Context, ch watch.PRChanges, reply string, logger *slog.Logger) {
	authors := conversationCommentAuthors(ch)
	if !hasConversationComment(ch) {
		return
	}
	body := reply
	if len(authors) > 0 {
		body = strings.Join(authors, " ") + "\n\n" + reply
	}
	if err := p.gh.PostComment(ctx, ch.PR.URL, body); err != nil {
		logger.Warn("post conversation reply failed", "err", err)
	}
}

func hasConversationComment(ch watch.PRChanges) bool {
	for _, ev := range ch.Events {
		if ev.Type == watch.EventComment && ev.Path == "" {
			return true
		}
	}
	return false
}

func conversationCommentAuthors(ch watch.PRChanges) []string {
	seen := map[string]bool{}
	var mentions []string
	for _, ev := range ch.Events {
		if ev.Type != watch.EventComment || ev.Path != "" || ev.Author.Login == "" {
			continue
		}
		if seen[ev.Author.Login] {
			continue
		}
		seen[ev.Author.Login] = true
		mentions = append(mentions, "@"+ev.Author.Login)
	}
	return mentions
}

// engagementSummary is a short human description of why Noctra is
// re-engaging on a PR — used in the Telegram heads-up and the commit message.
func engagementSummary(ch watch.PRChanges) string {
	hasFeedback := len(ch.Events) > 0
	hasCI := ch.CIFailure != nil
	switch {
	case hasFeedback && hasCI:
		return "addressing review + CI"
	case hasCI:
		return "fixing CI"
	default:
		return "addressing review"
	}
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

// identifierFromBranch turns "noctra/eng-42" into "ENG-42". Returns ""
// if the branch isn't a Noctra branch.
func identifierFromBranch(branch string) string {
	if !strings.HasPrefix(branch, "noctra/") {
		return ""
	}
	return strings.ToUpper(strings.TrimPrefix(branch, "noctra/"))
}
