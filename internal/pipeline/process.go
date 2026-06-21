package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

// maxReviewDiffBytes caps the diff sent to the optional Gemini review gate.
// Review prompts only need enough context to catch obvious issues; an
// unbounded diff can make the gate slow, expensive, or exceed model limits.
const maxReviewDiffBytes = 60000

// resolveBackend returns the coding-agent backend for a ticket. If the issue
// carries an "agent:<name>" label, that backend is used; otherwise the
// pipeline's default (p.agent) is returned. An unknown label value degrades
// to the default with a warning — never a hard failure.
func (p *Pipeline) resolveBackend(issue linear.Issue) agent.Backend {
	label := issue.BackendLabel()
	if label == "" {
		return p.agent
	}
	b, err := agent.New(label)
	if err != nil {
		slog.Warn("unknown backend label — using default",
			"id", issue.Identifier, "label", label, "default", p.agent.Name(), "err", err)
		return p.agent
	}
	slog.Info("per-ticket backend from label",
		"id", issue.Identifier, "backend", b.Name(), "label", label)
	return b
}

// process is one ticket's full lifecycle: resolve repo → worktree → run
// Claude → check output → (optional) Gemini review → commit/push → create PR
// → update Linear. Each failure mode posts a Linear comment and moves the
// ticket back to the trigger state, mirroring the bash predecessor exactly.
func (p *Pipeline) process(ctx context.Context, issue linear.Issue) {
	id := issue.Identifier
	logger := slog.With("id", id)
	logger.Info("starting", "title", issue.Title)

	// Select the coding-agent backend for this ticket — an "agent:<name>"
	// label overrides the configured default.
	backend := p.resolveBackend(issue)

	// ── Plan-confirm gate (ENG-221) ──────────────────────────────────────────
	// When plan-confirm is active for this ticket and no approved plan is
	// queued, run a plan-only pass instead of implementing. The plan is posted
	// as a Linear comment and the ticket stays in the trigger state until a
	// human approves.
	p.mu.Lock()
	_, hasApprovedPlan := p.approvedPlans[id]
	p.mu.Unlock()
	if p.needsPlanConfirm(issue) && !hasApprovedPlan {
		p.processPlanOnly(ctx, issue)
		return
	}

	if p.cfg.TelegramVerbose {
		p.notifier.Send(ctx, fmt.Sprintf("🎯 *%s* — %s\nNoctra picked it up — working on it.",
			id, notify.EscapeMarkdown(issue.Title)))
	}

	logFile := filepath.Join(p.cfg.LogDir, id+".log")
	if err := agent.AttemptHeader(logFile); err != nil {
		logger.Warn("could not write attempt header", "err", err)
	}

	// ── Resolve target repo ──────────────────────────────────────────────────
	// A "Repo:" directive on the ticket's Linear project is the primary route;
	// otherwise fall back to the REPO_PATH single-repo setting (if configured).
	var (
		resolved repo.Resolved
		err      error
	)
	if ref, branch := issue.Project.RepoDirective(); ref != "" {
		logger.Info("repo from Linear project directive", "repo", ref, "branch", branch)
		resolved, err = p.resolver.ResolveDirect(ctx, ref, branch)
	} else {
		resolved, err = p.resolver.Resolve(ctx, issue.ProjectName())
	}
	if err != nil {
		logger.Error("repo resolution failed", "err", err)

		var nte *repo.NonTransientError
		if errors.As(err, &nte) {
			// Deterministic config error — will never succeed without
			// human intervention. Skip on future polls and refund the
			// dispatch so config mistakes don't burn the budget or shut
			// the agent down. Only one comment + notification is posted.
			p.skipPermanently(id)
			if cerr := p.linear.Comment(ctx, issue.ID,
				fmt.Sprintf("❌ **Noctra: No repo for this ticket**\n\n%s\n\nAdd a `Repo: owner/name` line to this ticket's **Linear project description** (optionally a `Branch:` line). Then move it back to **%s**.",
					err.Error(), p.cfg.TriggerState)); cerr != nil {
				slog.Warn("linear Comment failed", "issue_id", issue.ID, "err", cerr)
			}
			p.notifier.Send(ctx, fmt.Sprintf("⚠️ *%s* — skipped (no repo mapping)", id))
			return
		}

		// Transient failure — move back to trigger and retry (bounded).
		attempts := p.bumpFailed(id)
		var msg string
		if attempts >= p.cfg.MaxRetries {
			msg = fmt.Sprintf("❌ **Noctra: repo resolution failed** (attempt %d/%d)\n\n%s\n\nMax retries reached. Ticket moved back to **%s** but will not be retried automatically.",
				attempts, p.cfg.MaxRetries, err.Error(), p.cfg.TriggerState)
		} else {
			msg = fmt.Sprintf("❌ **Noctra: repo resolution failed** (attempt %d/%d)\n\n%s\n\nTicket moved back to **%s**. Will retry on next poll cycle.",
				attempts, p.cfg.MaxRetries, err.Error(), p.cfg.TriggerState)
		}
		p.linearBackToTrigger(ctx, issue.ID, msg)
		return
	}
	logger.Info("repo resolved", "path", resolved.Path, "main", resolved.MainBranch)

	// ── Create worktree ──────────────────────────────────────────────────────
	wt, err := repo.CreateWorktree(ctx, p.cfg.WorktreeBase, id, resolved.Path, resolved.MainBranch)
	if err != nil {
		logger.Error("worktree creation failed", "err", err)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"❌ **Noctra: Setup failed**\n\nCould not create worktree. This may be a branch naming conflict.\n\nCheck that branch `%s` does not already exist on the remote.\n\nTicket moved back to **%s**.",
			repo.BranchName(id), p.cfg.TriggerState))
		return
	}
	logger.Info("worktree", "path", wt.Path, "branch", wt.Branch)

	// ── Run Claude ───────────────────────────────────────────────────────────
	promptInput := agent.BuildPromptInput{
		Identifier:       id,
		Title:            issue.Title,
		Description:      issue.Description,
		Comments:         issue.ClarificationComments(),
		UseTeams:         p.cfg.UseAgentTeams,
		AutoReleaseLabel: p.cfg.AutoReleaseLabel,
	}
	var prompt string
	p.mu.Lock()
	approvedPlan := p.approvedPlans[id]
	p.mu.Unlock()
	if approvedPlan != "" {
		prompt = agent.BuildPlanImplementPrompt(promptInput, approvedPlan)
		logger.Info("using approved plan as implementation context")
	} else {
		prompt = agent.BuildPrompt(promptInput)
	}

	logger.Info("running agent",
		"backend", backend.Name(),
		"log", logFile,
		"timeout", p.cfg.AgentTimeout,
		"agent_teams", p.cfg.UseAgentTeams)

	// CRITICAL: record the log size BEFORE the agent runs so subsequent
	// BLOCKED / rate-limit checks only inspect this attempt's output.
	offset := agent.OffsetBefore(logFile)

	runErr := backend.Run(ctx, agent.RunOptions{
		Workdir:       wt.Path,
		Prompt:        prompt,
		LogFile:       logFile,
		Timeout:       p.cfg.AgentTimeout,
		UseAgentTeams: p.cfg.UseAgentTeams,
	})

	// ── Killed via /kill ────────────────────────────────────────────────────
	if p.isKilled(id) {
		logger.Info("run killed by user")
		repo.CleanupWorktree(context.Background(), resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── Context cancelled (graceful shutdown) ────────────────────────────────
	if ctx.Err() != nil {
		logger.Info("run cancelled (shutdown)", "reason", ctx.Err())
		repo.CleanupWorktree(context.Background(), resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── Timeout ──────────────────────────────────────────────────────────────
	if errors.Is(runErr, agent.ErrTimedOut) {
		logger.Warn("timed out", "timeout", p.cfg.AgentTimeout)
		p.bumpFailed(id)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"⏰ **Noctra: Agent timed out**\n\nClaude timed out after %s working on this ticket.\n\nThe ticket may be too complex for a single session. Consider breaking it into smaller tasks.\n\nTicket moved back to **%s**.",
			p.cfg.AgentTimeout, p.cfg.TriggerState))
		p.notifier.Send(ctx, fmt.Sprintf("⏰ *%s* — %s\nTimed out after %s. Moving back to %s.",
			id, notify.EscapeMarkdown(issue.Title), p.cfg.AgentTimeout, notify.EscapeMarkdown(p.cfg.TriggerState)))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	output := agent.ReadAfter(logFile, offset)

	// ── Record usage (ENG-217) ───────────────────────────────────────────────
	usage := agent.ParseUsage(output)
	p.budget.Record(usage.TotalTokens, usage.CostUSD)
	if usage.TotalTokens > 0 || usage.CostUSD > 0 {
		logger.Info("usage recorded",
			"tokens", usage.TotalTokens, "cost_usd", usage.CostUSD)
	}
	// Check budget caps after recording. If exceeded, the pause takes effect
	// on the next poll tick (in-flight work drains naturally).
	if reason := p.budget.ExceededReason(); reason != "" {
		p.flagBudgetExceeded(reason)
		p.notifier.Send(ctx, fmt.Sprintf(
			"⏸ *Daily budget exceeded*\n%s\nDispatching paused until next UTC midnight.",
			notify.EscapeMarkdown(reason)))
	}

	// ── Rate limit ───────────────────────────────────────────────────────────
	// Only ever classified on a FAILED run (see rateLimited): scanning the
	// transcript of a *successful* run caused false positives where an agent
	// legitimately writing the words "rate limit" into a file got its completed
	// work discarded (ENG-178 — Codex built the Noctra landing page, whose
	// copy advertises "rate-limit detection"; three good runs were thrown away).
	if rateLimited(backend, runErr, output) {
		logger.Warn("usage/rate limit detected")
		p.bumpFailed(id)
		p.flagRateLimit()

		action := "pausing"
		if p.cfg.RateLimitStrategy == "shutdown" {
			action = "shutting down"
		}
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"🛑 **Noctra: Rate limit detected**\n\nThe agent hit a usage or rate limit while working on this ticket.\n\nTicket moved back to **%s**. Noctra is %s to avoid further limit hits.",
			p.cfg.TriggerState, action))
		p.mu.Lock()
		s, f, t := p.successCount, p.failCount, p.totalDispatches
		p.mu.Unlock()
		p.notifier.Send(ctx, fmt.Sprintf(
			"🛑 *Usage limit detected*\nNoctra %s after %d dispatches.\n✅ %d PRs created | ❌ %d failed",
			action, t, s, f))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── Non-zero exit ────────────────────────────────────────────────────────
	if runErr != nil {
		attempts := p.bumpFailed(id)
		logger.Warn("agent exited with error",
			"err", runErr, "attempt", attempts, "max", p.cfg.MaxRetries)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"❌ **Noctra: Agent failed** (attempt %d/%d)\n\nThe agent exited with an error. Will retry on next poll cycle (up to %d attempts).\n\nTicket moved back to **%s**.",
			attempts, p.cfg.MaxRetries, p.cfg.MaxRetries, p.cfg.TriggerState))
		p.notifier.Send(ctx, fmt.Sprintf("❌ *%s* — %s\nFailed (attempt %d/%d)",
			id, notify.EscapeMarkdown(issue.Title), attempts, p.cfg.MaxRetries))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── BLOCKED ──────────────────────────────────────────────────────────────
	// Count the attempt: the ticket is left in the trigger state, so without a
	// retry cap it would be re-dispatched every poll until the dispatch budget
	// is gone. After MaxRetries it's skipped until restart / human input.
	if blocked := agent.BlockedLine(output); blocked != "" {
		attempts := p.bumpFailed(id)
		logger.Info("blocked", "line", blocked, "attempt", attempts, "max", p.cfg.MaxRetries)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"🚧 **Noctra needs your input** (attempt %d/%d)\n\nThe agent got blocked on this ticket:\n\n> %s\n\nClarify in the ticket comments, then move it back to **%s** to retry. After %d attempts it won't be re-dispatched until Noctra restarts.",
			attempts, p.cfg.MaxRetries, blocked, p.cfg.TriggerState, p.cfg.MaxRetries))
		p.notifier.Send(ctx, fmt.Sprintf("⚠️ *%s* — Blocked\n%s", id, notify.EscapeMarkdown(blocked)))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── Any changes to commit? ───────────────────────────────────────────────
	// Claude may leave edits uncommitted OR commit them itself, so "did it do
	// anything" means a dirty worktree OR commits ahead of the base branch.
	// Checking only the worktree would bounce a perfectly good self-committed
	// implementation as "no changes made" (ENG-182).
	dirty, err := workingTreeChanged(ctx, wt.Path)
	if err != nil {
		logger.Error("git status failed", "err", err)
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}
	committed, err := branchAhead(ctx, wt.Path, "origin/"+resolved.MainBranch)
	if err != nil {
		logger.Error("git rev-list failed", "err", err)
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}
	if !dirty && !committed {
		// Count the attempt — otherwise this re-queues to the trigger state every
		// poll and burns the whole dispatch budget on one unprogressable ticket
		// (e.g. a vague description, already-done work, or a ticket pointed at the
		// wrong repo). After MaxRetries it's skipped until restart.
		attempts := p.bumpFailed(id)
		logger.Warn("no changes made", "attempt", attempts, "max", p.cfg.MaxRetries)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"💭 **Noctra: No code changes made** (attempt %d/%d)\n\nThe agent completed without modifying any files — usually the ticket is too vague, already done, or its Linear project points at the wrong repo.\n\nAdd more detail (or check the project's `Repo:` directive), then move it back to **%s** to retry. After %d attempts it won't be re-dispatched until Noctra restarts.",
			attempts, p.cfg.MaxRetries, p.cfg.TriggerState, p.cfg.MaxRetries))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	if err := runIn(ctx, wt.Path, "git", "add", "-A"); err != nil {
		logger.Error("git add failed", "err", err)
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── Gemini review gate (optional) ────────────────────────────────────────
	reviewPassed := true
	reviewSkipped := false
	var reviewBody string
	reviewAttempts := 0

	if p.review.Enabled() {
		logger.Info("running gemini review gate")
		for i := 0; i <= p.cfg.MaxReviewRetries; i++ {
			reviewAttempts = i + 1
			diff := boundedReviewDiff(gitDiff(ctx, wt.Path))
			r, err := p.review.Review(ctx, issue.Title, issue.Description, diff)
			if err != nil {
				if errors.Is(err, review.ErrUnavailable) {
					logger.Warn("gemini review gate skipped", "err", err)
					reviewPassed = true
					reviewSkipped = true
					reviewBody = r.Body
					break
				}
				logger.Warn("gemini review request failed", "err", err)
				reviewPassed = false
				reviewBody = err.Error()
				break
			}
			reviewBody = r.Body
			if r.Skipped {
				reviewPassed = true
				reviewSkipped = true
				logger.Warn("gemini review gate skipped", "reason", r.Body)
				break
			}
			if r.Passed {
				reviewPassed = true
				logger.Info("✅ gemini review passed")
				break
			}
			reviewPassed = false
			logger.Info("🔄 gemini flagged issues", "attempt", i+1, "of", p.cfg.MaxReviewRetries+1)

			if i < p.cfg.MaxReviewRetries {
				fixPrompt := fmt.Sprintf(`A code reviewer found issues with your implementation. Please fix them.

## Reviewer feedback:
%s

## Rules:
- Only address the specific issues mentioned in the feedback above.
- Do not change anything else.
- Run tests after fixing to make sure nothing broke.`, r.Body)

				logger.Info("asking the agent to fix review issues")
				fixOffset := agent.OffsetBefore(logFile)
				fixErr := backend.Run(ctx, agent.RunOptions{
					Workdir:       wt.Path,
					Prompt:        fixPrompt,
					LogFile:       logFile,
					Timeout:       p.cfg.AgentTimeout,
					UseAgentTeams: p.cfg.UseAgentTeams,
				})

				if p.isKilled(id) {
					logger.Info("fix-pass killed by user")
					repo.CleanupWorktree(context.Background(), resolved.Path, p.cfg.WorktreeBase, id)
					return
				}
				if ctx.Err() != nil {
					logger.Info("fix-pass cancelled (shutdown)", "reason", ctx.Err())
					repo.CleanupWorktree(context.Background(), resolved.Path, p.cfg.WorktreeBase, id)
					return
				}

				fixOutput := agent.ReadAfter(logFile, fixOffset)

				// Record usage from the fix pass.
				fixUsage := agent.ParseUsage(fixOutput)
				p.budget.Record(fixUsage.TotalTokens, fixUsage.CostUSD)
				if reason := p.budget.ExceededReason(); reason != "" {
					p.flagBudgetExceeded(reason)
					p.notifier.Send(ctx, fmt.Sprintf(
						"⏸ *Daily budget exceeded*\n%s\nDispatching paused until next UTC midnight.",
						notify.EscapeMarkdown(reason)))
				}

				switch classifyAgentRun(backend, fixErr, fixOutput) {
				case agentRunTimedOut:
					logger.Warn("fix-pass timed out", "timeout", p.cfg.AgentTimeout)
					p.bumpFailed(id)
					p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
						"⏰ **Noctra: Agent timed out**\n\n%s timed out after %s while fixing Gemini review feedback.\n\nTicket moved back to **%s**.",
						backend.Label(), p.cfg.AgentTimeout, p.cfg.TriggerState))
					repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
					return
				case agentRunRateLimited:
					logger.Warn("usage/rate limit detected during fix-pass — triggering shutdown")
					p.bumpFailed(id)
					p.flagRateLimit()
					p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
						"🛑 **Noctra: Rate limit detected**\n\nThe agent hit a usage or rate limit while fixing Gemini review feedback.\n\nTicket moved back to **%s**. Noctra is shutting down to avoid further limit hits.",
						p.cfg.TriggerState))
					repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
					return
				case agentRunFailed:
					logger.Warn("fix-pass exited with error", "err", fixErr)
				}
				_ = runIn(ctx, wt.Path, "git", "add", "-A")
			}
		}
		if !reviewPassed {
			logger.Warn("gemini did not pass — creating PR with review comments attached",
				"attempts", reviewAttempts)
		}
	}

	// ── Commit and push ──────────────────────────────────────────────────────
	// Commit only if there's staged work — if Claude already committed its own
	// changes, the staged set is empty and a plain `git commit` would fail with
	// "nothing to commit" and bounce a valid implementation (ENG-182).
	// In Conventional Commits repos, derive the commit type from the agent's
	// release bump so subject + PR title drive release tooling. When no bump is
	// available (e.g. AUTO_RELEASE_LABEL off), fall back to DefaultReleaseBump
	// so a CC repo still gets a valid conventional type (else commitlint/CI
	// rejects the title). bump is reused for the release label below.
	bump := agent.ReleaseBump(output)
	usesCC := repo.UsesConventionalCommits(resolved.Path)
	if bump == "" && usesCC {
		bump = p.cfg.DefaultReleaseBump
	}
	ccType, breaking := conventionalType(bump)
	useCC := ccType != "" && usesCC

	prTitle := fmt.Sprintf("%s: %s", id, issue.Title)
	commitSubject := fmt.Sprintf("feat: implement %s — %s", id, issue.Title)
	if useCC {
		subj := conventionalSubject(ccType, breaking, issue.Title, id)
		prTitle, commitSubject = subj, subj
	}
	commitBody := fmt.Sprintf("Implemented by Noctra using %s\n\nLinear: %s", backend.Label(), issue.URL)
	if useCC && breaking {
		commitBody += fmt.Sprintf("\n\nBREAKING CHANGE: %s", issue.Title)
	}
	commitMsg := appendCoAuthorTrailer(commitSubject+"\n\n"+commitBody, backend.CoAuthor())

	staged, err := hasStagedChanges(ctx, wt.Path)
	if err != nil {
		logger.Error("git diff --cached failed", "err", err)
		p.linearBackToTrigger(ctx, issue.ID, "❌ **Noctra: commit check failed** — see Noctra logs.")
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}
	if staged {
		if err := runIn(ctx, wt.Path, "git", "commit", "-m", commitMsg); err != nil {
			logger.Error("git commit failed", "err", err)
			p.linearBackToTrigger(ctx, issue.ID, "❌ **Noctra: commit failed** — see Noctra logs.")
			repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
			return
		}
	}
	if err := runIn(ctx, wt.Path, "git", "push", "-u", "origin", wt.Branch); err != nil {
		logger.Error("git push failed", "err", err)
		p.linearBackToTrigger(ctx, issue.ID, "❌ **Noctra: push failed** — check that the host has push access and `gh auth status` is healthy.")
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}
	logger.Info("pushed", "branch", wt.Branch)

	// ── PR body ──────────────────────────────────────────────────────────────
	rawLog, _ := os.ReadFile(logFile)
	summary := agent.ExtractSummary(string(rawLog))

	reviewSection := ""
	if p.review.Enabled() {
		if reviewSkipped {
			reviewSection = fmt.Sprintf("\n---\n\n⚠️ **Multi-model review:** Skipped (Gemini `%s` via `%s`)\n\n%s",
				p.review.Model, p.review.Mode, reviewBody)
		} else if reviewPassed {
			reviewSection = fmt.Sprintf("\n---\n\n✅ **Multi-model review:** Passed (Gemini `%s` via `%s`)",
				p.review.Model, p.review.Mode)
		} else {
			reviewSection = fmt.Sprintf(
				"\n\n---\n\n⚠️ **Multi-model review:** Did not pass after %d attempt(s). Please review before merging:\n\n<details>\n<summary>Gemini review comments</summary>\n\n```\n%s\n```\n\n</details>",
				reviewAttempts, reviewBody)
		}
	}

	prBody := fmt.Sprintf(
		"## %s: %s\n\n**Linear:** %s\n\n## What was implemented\n\n%s\n%s\n---\n\n*Implemented by [Noctra](https://github.com/ahmadAlMezaal/noctra) 🌙 using %s*\n%s",
		id, issue.Title, issue.URL, summary, reviewSection, backend.Label(), github.NoctraPRBodyMarker)

	// ── gh pr create ─────────────────────────────────────────────────────────
	prURL, err := ghCreatePR(ctx, resolved.Path,
		prTitle,
		prBody, resolved.MainBranch, wt.Branch)
	if err != nil {
		logger.Error("gh pr create failed", "err", err)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"❌ **Noctra: PR creation failed**\n\nThe branch `%s` was pushed, but `gh pr create` failed.\n\nCheck that you have push access to the repository and that `gh` is authenticated.\n\nError:\n```\n%s\n```\n\nTicket moved back to **%s**.",
			wt.Branch, err.Error(), p.cfg.TriggerState))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── Auto-release label (ENG-231) ────────────────────────────────────────
	if p.cfg.AutoReleaseLabel {
		label := agent.ReleaseLabel(bump, p.cfg.DefaultReleaseBump)
		if label != "" {
			if err := ghAddLabel(ctx, resolved.Path, prURL, label); err != nil {
				// Degrade gracefully: label failure never blocks the PR.
				logger.Warn("could not apply release label", "label", label, "err", err)
			} else {
				logger.Info("applied release label", "label", label, "agent_bump", bump)
			}
		} else {
			logger.Info("release label skipped (agent suggested none)")
		}
	}

	logger.Info("✅ PR created", "url", prURL)
	p.bumpSuccess()
	p.notifier.Send(ctx, fmt.Sprintf("✅ *%s* — %s\nPR ready (via %s): %s",
		id, notify.EscapeMarkdown(issue.Title), notify.EscapeMarkdown(backend.Label()), prURL))

	// Persist the chosen backend so auto-iterate uses the same one for
	// follow-up commits on this PR.
	if p.store != nil {
		if err := p.store.Update(prURL, func(r *state.PRState) {
			r.TicketID = id
			r.AgentBackend = backend.Name()
		}); err != nil {
			logger.Warn("could not persist backend in PR state", "err", err)
		}
	}

	// ── Move to In Review + remove trigger label ────────────────────────────
	if p.cfg.TriggerMode == "label" && p.labelID != "" {
		if err := p.linear.RemoveLabel(ctx, issue.ID, p.labelID); err != nil {
			logger.Warn("could not remove trigger label", "err", err)
		}
	}
	if err := p.linear.SetState(ctx, issue.ID, p.states.InReview); err != nil {
		logger.Warn("could not set in-review state", "err", err)
	}
	if err := p.linear.Comment(ctx, issue.ID, fmt.Sprintf(
		"🌙 **Noctra created a PR** (via %s)\n\n**PR:** %s\n\nMoved to **%s**. Ready for your review!",
		backend.Label(), prURL, p.cfg.InReviewState)); err != nil {
		logger.Warn("could not post Linear comment", "err", err)
	}

	logger.Info("done", "next_state", p.cfg.InReviewState)
	repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
}

// linearBackToTrigger moves a ticket back to the trigger state and posts the
// given comment, swallowing API errors (the operation is best-effort). In
// label mode the trigger label is already present (not removed until
// success), so only the comment is posted.
func (p *Pipeline) linearBackToTrigger(ctx context.Context, issueID, body string) {
	if p.states.Trigger != "" {
		if err := p.linear.SetState(ctx, issueID, p.states.Trigger); err != nil {
			slog.Warn("linear SetState failed", "issue_id", issueID, "err", err)
		}
	}
	if err := p.linear.Comment(ctx, issueID, body); err != nil {
		slog.Warn("linear Comment failed", "issue_id", issueID, "err", err)
	}
}

func workingTreeChanged(ctx context.Context, workdir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// hasStagedChanges reports whether the index has anything to commit. Used so we
// only commit when there's staged work — Claude sometimes commits its own
// changes, in which case a `git commit` would fail with "nothing to commit".
func hasStagedChanges(ctx context.Context, workdir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false, nil // exit 0: index clean
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return true, nil // exit 1: staged changes present
	}
	return false, fmt.Errorf("git diff --cached: %w (%s)", err, strings.TrimSpace(string(out)))
}

// branchAhead reports whether HEAD has commits not present in upstream (e.g.
// origin/main, or origin/<branch>). This is how we detect work to push even
// when Claude committed it itself, leaving a clean working tree.
func branchAhead(ctx context.Context, workdir, upstream string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-list", "--count", upstream+"..HEAD")
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git rev-list %s..HEAD: %w (%s)", upstream, err, strings.TrimSpace(string(out)))
	}
	n := strings.TrimSpace(string(out))
	return n != "" && n != "0", nil
}

// gitDiff returns the staged diff (or the working-tree diff against HEAD if
// nothing is staged), used as input to the Gemini review gate.
func gitDiff(ctx context.Context, workdir string) string {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached")
	cmd.Dir = workdir
	out, _ := cmd.Output()
	if len(bytes.TrimSpace(out)) > 0 {
		return string(out)
	}
	cmd2 := exec.CommandContext(ctx, "git", "diff", "HEAD")
	cmd2.Dir = workdir
	out2, _ := cmd2.Output()
	return string(out2)
}

func boundedReviewDiff(diff string) string {
	if len(diff) <= maxReviewDiffBytes {
		return diff
	}
	headLen := maxReviewDiffBytes / 2
	tailLen := maxReviewDiffBytes - headLen
	headEnd := safeForwardBoundary(diff, headLen)
	tailStart := safeBackwardBoundary(diff, len(diff)-tailLen)
	return fmt.Sprintf("%s\n\n... (diff truncated for review: showing first %d and last %d bytes of %d total bytes) ...\n\n%s",
		diff[:headEnd], headEnd, len(diff)-tailStart, len(diff), diff[tailStart:])
}

func safeForwardBoundary(s string, end int) int {
	if end >= len(s) {
		return len(s)
	}
	for end > 0 && s[end]&0xC0 == 0x80 {
		end--
	}
	return end
}

func safeBackwardBoundary(s string, start int) int {
	if start <= 0 {
		return 0
	}
	for start < len(s) && s[start]&0xC0 == 0x80 {
		start++
	}
	return start
}

type agentRunStatus int

const (
	agentRunOK agentRunStatus = iota
	agentRunTimedOut
	agentRunRateLimited
	agentRunFailed
)

func classifyAgentRun(b agent.Backend, runErr error, output string) agentRunStatus {
	if errors.Is(runErr, agent.ErrTimedOut) {
		return agentRunTimedOut
	}
	if rateLimited(b, runErr, output) {
		return agentRunRateLimited
	}
	if runErr != nil {
		return agentRunFailed
	}
	return agentRunOK
}

// ghCreatePR runs `gh pr create` and returns the URL printed by gh.
func ghCreatePR(ctx context.Context, repoPath, title, body, base, head string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "create",
		"--title", title,
		"--body", body,
		"--base", base,
		"--head", head,
	)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ghAddLabel applies a label to an existing PR via `gh pr edit --add-label`.
// prURL is the PR URL returned by ghCreatePR. Errors are returned to the
// caller for logging but never block the PR.
func ghAddLabel(ctx context.Context, repoPath, prURL, label string) error {
	cmd := exec.CommandContext(ctx, "gh", "pr", "edit", prURL, "--add-label", label)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitHeadShort returns the abbreviated HEAD commit SHA, or "" on error.
func gitHeadShort(ctx context.Context, workdir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runIn(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appendCoAuthorTrailer appends a Co-authored-by trailer to a commit message
// body when the backend declares one. The trailer is separated from the body
// by a blank line, following git's trailer convention. Returns the message
// unchanged when coAuthor is empty.
func appendCoAuthorTrailer(msg, coAuthor string) string {
	if coAuthor == "" {
		return msg
	}
	return strings.TrimRight(msg, " \t\n\r") + "\n\nCo-authored-by: " + coAuthor
}
