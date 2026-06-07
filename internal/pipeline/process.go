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

	"github.com/ahmadAlMezaal/nightshift/internal/agent"
	"github.com/ahmadAlMezaal/nightshift/internal/linear"
	"github.com/ahmadAlMezaal/nightshift/internal/notify"
	"github.com/ahmadAlMezaal/nightshift/internal/repo"
)

// process is one ticket's full lifecycle: resolve repo → worktree → run
// Claude → check output → (optional) Gemini review → commit/push → create PR
// → update Linear. Each failure mode posts a Linear comment and moves the
// ticket back to the trigger state, mirroring the bash predecessor exactly.
func (p *Pipeline) process(ctx context.Context, issue linear.Issue) {
	id := issue.Identifier
	logger := slog.With("id", id)
	logger.Info("starting", "title", issue.Title)

	if p.cfg.TelegramVerbose {
		p.telegram.Send(ctx, fmt.Sprintf("🎯 *%s* — %s\nNightshift picked it up — working on it.",
			id, notify.EscapeMarkdown(issue.Title)))
	}

	logFile := filepath.Join(p.cfg.LogDir, id+".log")
	if err := agent.AttemptHeader(logFile); err != nil {
		logger.Warn("could not write attempt header", "err", err)
	}

	// ── Resolve target repo from the ticket's Linear project ─────────────────
	resolved, err := p.resolver.Resolve(ctx, issue.ProjectName())
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
				fmt.Sprintf("❌ **Nightshift: No repo for this ticket**\n\n%s\n\nMap this ticket's project in `repos.json` (or run `./nightshift setup`), then restart Nightshift and move it back to **%s**.",
					err.Error(), p.cfg.TriggerState)); cerr != nil {
				slog.Warn("linear Comment failed", "issue_id", issue.ID, "err", cerr)
			}
			p.telegram.Send(ctx, fmt.Sprintf("⚠️ *%s* — skipped (no repo mapping)", id))
			return
		}

		// Transient failure — move back to trigger and retry (bounded).
		attempts := p.bumpFailed(id)
		var msg string
		if attempts >= p.cfg.MaxRetries {
			msg = fmt.Sprintf("❌ **Nightshift: repo resolution failed** (attempt %d/%d)\n\n%s\n\nMax retries reached. Ticket moved back to **%s** but will not be retried automatically.",
				attempts, p.cfg.MaxRetries, err.Error(), p.cfg.TriggerState)
		} else {
			msg = fmt.Sprintf("❌ **Nightshift: repo resolution failed** (attempt %d/%d)\n\n%s\n\nTicket moved back to **%s**. Will retry on next poll cycle.",
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
			"❌ **Nightshift: Setup failed**\n\nCould not create worktree. This may be a branch naming conflict.\n\nCheck that branch `%s` does not already exist on the remote.\n\nTicket moved back to **%s**.",
			repo.BranchName(id), p.cfg.TriggerState))
		return
	}
	logger.Info("worktree", "path", wt.Path, "branch", wt.Branch)

	// ── Run Claude ───────────────────────────────────────────────────────────
	prompt := agent.BuildPrompt(agent.BuildPromptInput{
		Identifier:  id,
		Title:       issue.Title,
		Description: issue.Description,
		UseTeams:    p.cfg.UseAgentTeams,
	})

	logger.Info("running agent",
		"backend", p.agent.Name(),
		"timeout", p.cfg.AgentTimeout,
		"agent_teams", p.cfg.UseAgentTeams)

	// CRITICAL: record the log size BEFORE the agent runs so subsequent
	// BLOCKED / rate-limit checks only inspect this attempt's output.
	offset := agent.OffsetBefore(logFile)

	runErr := p.agent.Run(ctx, agent.RunOptions{
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
			"⏰ **Nightshift: Agent timed out**\n\nClaude timed out after %s working on this ticket.\n\nThe ticket may be too complex for a single session. Consider breaking it into smaller tasks.\n\nTicket moved back to **%s**.",
			p.cfg.AgentTimeout, p.cfg.TriggerState))
		p.telegram.Send(ctx, fmt.Sprintf("⏰ *%s* — %s\nTimed out after %s. Moving back to %s.",
			id, notify.EscapeMarkdown(issue.Title), p.cfg.AgentTimeout, notify.EscapeMarkdown(p.cfg.TriggerState)))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	output := agent.ReadAfter(logFile, offset)

	// ── Rate limit ───────────────────────────────────────────────────────────
	if p.agent.HasRateLimit(output) {
		logger.Warn("usage/rate limit detected — triggering shutdown")
		p.bumpFailed(id)
		p.flagRateLimit()
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"🛑 **Nightshift: Rate limit detected**\n\nClaude hit a usage or rate limit while working on this ticket.\n\nTicket moved back to **%s**. Nightshift is shutting down to avoid further limit hits.",
			p.cfg.TriggerState))
		p.mu.Lock()
		s, f, t := p.successCount, p.failCount, p.totalDispatches
		p.mu.Unlock()
		p.telegram.Send(ctx, fmt.Sprintf(
			"🛑 *Usage limit detected*\nNightshift stopped after %d dispatches.\n✅ %d PRs created | ❌ %d failed",
			t, s, f))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── Non-zero exit ────────────────────────────────────────────────────────
	if runErr != nil {
		attempts := p.bumpFailed(id)
		logger.Warn("claude exited with error",
			"err", runErr, "attempt", attempts, "max", p.cfg.MaxRetries)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"❌ **Nightshift: Agent failed** (attempt %d/%d)\n\nClaude exited with an error. Will retry on next poll cycle (up to %d attempts).\n\nTicket moved back to **%s**.",
			attempts, p.cfg.MaxRetries, p.cfg.MaxRetries, p.cfg.TriggerState))
		p.telegram.Send(ctx, fmt.Sprintf("❌ *%s* — %s\nFailed (attempt %d/%d)",
			id, notify.EscapeMarkdown(issue.Title), attempts, p.cfg.MaxRetries))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	// ── BLOCKED ──────────────────────────────────────────────────────────────
	if blocked := agent.BlockedLine(output); blocked != "" {
		logger.Info("blocked", "line", blocked)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"🚧 **Nightshift needs your input**\n\nClaude got blocked on this ticket:\n\n> %s\n\nPlease clarify in the ticket comments, then move back to **%s** to retry.",
			blocked, p.cfg.TriggerState))
		p.telegram.Send(ctx, fmt.Sprintf("⚠️ *%s* — Blocked\n%s", id, notify.EscapeMarkdown(blocked)))
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
		logger.Warn("no changes made")
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"💭 **Nightshift: No code changes made**\n\nClaude completed the session without modifying any files. This usually means the ticket description is too vague or needs more context.\n\nAdd more detail to the ticket description and move back to **%s** to retry.",
			p.cfg.TriggerState))
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
	var reviewBody string
	reviewAttempts := 0

	if p.review.Enabled() {
		logger.Info("running gemini review gate")
		for i := 0; i <= p.cfg.MaxReviewRetries; i++ {
			reviewAttempts = i + 1
			diff := gitDiff(ctx, wt.Path)
			r, err := p.review.Review(ctx, issue.Title, issue.Description, diff)
			if err != nil {
				logger.Warn("gemini review request failed", "err", err)
				reviewPassed = false
				reviewBody = err.Error()
				break
			}
			reviewBody = r.Body
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
				if err := p.agent.Run(ctx, agent.RunOptions{
					Workdir:       wt.Path,
					Prompt:        fixPrompt,
					LogFile:       logFile,
					Timeout:       p.cfg.AgentTimeout,
					UseAgentTeams: p.cfg.UseAgentTeams,
				}); err != nil {
					logger.Warn("fix-pass exited with error", "err", err)
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
	commitMsg := fmt.Sprintf(
		"feat: implement %s — %s\n\nImplemented by Nightshift using Claude Code\n\nLinear: %s",
		id, issue.Title, issue.URL)

	staged, err := hasStagedChanges(ctx, wt.Path)
	if err != nil {
		logger.Error("git diff --cached failed", "err", err)
		p.linearBackToTrigger(ctx, issue.ID, "❌ **Nightshift: commit check failed** — see Nightshift logs.")
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}
	if staged {
		if err := runIn(ctx, wt.Path, "git", "commit", "-m", commitMsg); err != nil {
			logger.Error("git commit failed", "err", err)
			p.linearBackToTrigger(ctx, issue.ID, "❌ **Nightshift: commit failed** — see Nightshift logs.")
			repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
			return
		}
	}
	if err := runIn(ctx, wt.Path, "git", "push", "-u", "origin", wt.Branch); err != nil {
		logger.Error("git push failed", "err", err)
		p.linearBackToTrigger(ctx, issue.ID, "❌ **Nightshift: push failed** — check that the host has push access and `gh auth status` is healthy.")
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}
	logger.Info("pushed", "branch", wt.Branch)

	// ── PR body ──────────────────────────────────────────────────────────────
	rawLog, _ := os.ReadFile(logFile)
	summary := agent.ExtractSummary(string(rawLog))

	reviewSection := ""
	if p.review.Enabled() {
		if reviewPassed {
			reviewSection = fmt.Sprintf("\n---\n\n✅ **Multi-model review:** Passed (Gemini `%s`)", p.cfg.GeminiModel)
		} else {
			reviewSection = fmt.Sprintf(
				"\n\n---\n\n⚠️ **Multi-model review:** Did not pass after %d attempt(s). Please review before merging:\n\n<details>\n<summary>Gemini review comments</summary>\n\n```\n%s\n```\n\n</details>",
				reviewAttempts, reviewBody)
		}
	}

	prBody := fmt.Sprintf(
		"## %s: %s\n\n**Linear:** %s\n\n## What was implemented\n\n%s\n%s\n---\n\n*Implemented by [Nightshift](https://github.com/ahmadAlMezaal/nightshift) 🌙 using Claude Code*",
		id, issue.Title, issue.URL, summary, reviewSection)

	// ── gh pr create ─────────────────────────────────────────────────────────
	prURL, err := ghCreatePR(ctx, resolved.Path,
		fmt.Sprintf("%s: %s", id, issue.Title),
		prBody, resolved.MainBranch, wt.Branch)
	if err != nil {
		logger.Error("gh pr create failed", "err", err)
		p.linearBackToTrigger(ctx, issue.ID, fmt.Sprintf(
			"❌ **Nightshift: PR creation failed**\n\nThe branch `%s` was pushed, but `gh pr create` failed.\n\nCheck that you have push access to the repository and that `gh` is authenticated.\n\nError:\n```\n%s\n```\n\nTicket moved back to **%s**.",
			wt.Branch, err.Error(), p.cfg.TriggerState))
		repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	logger.Info("✅ PR created", "url", prURL)
	p.bumpSuccess()
	p.telegram.Send(ctx, fmt.Sprintf("✅ *%s* — %s\nPR ready: %s", id, notify.EscapeMarkdown(issue.Title), prURL))

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
		"🌙 **Nightshift created a PR**\n\n**PR:** %s\n\nMoved to **%s**. Ready for your review!",
		prURL, p.cfg.InReviewState)); err != nil {
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
