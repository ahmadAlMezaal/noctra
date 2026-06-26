package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/state"
	"github.com/ahmadAlMezaal/noctra/internal/sweep"
)

// runSweepLoop is the sweep-scheduler loop, started by Run when
// cfg.SweepEnabled is true. It runs on the same WaitGroup as the main
// Linear poll loop so shutdown drains in-flight sweep tasks.
func (p *Pipeline) runSweepLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	slog.Info("sweep scheduler starting",
		"interval", p.cfg.SweepInterval,
		"max_tasks", p.cfg.SweepMaxTasks,
	)

	for {
		due := p.sweeper.DueIn()
		if due > 0 {
			slog.Debug("sweep: next sweep in", "due_in", due)
			timer := time.NewTimer(due)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		if ctx.Err() != nil {
			return
		}

		// Check budget pause before sweeping — wait for the pause to
		// expire rather than skipping the entire sweep interval.
		if paused, until, reason := p.budget.IsPaused(); paused {
			slog.Debug("sweep: paused, waiting for resume", "reason", reason, "until", until)
			retryIn := time.Until(until)
			if retryIn < 10*time.Second {
				retryIn = 10 * time.Second
			}
			timer := time.NewTimer(retryIn)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if reason := p.budget.ExceededReason(); reason != "" {
			slog.Debug("sweep: skipping (budget exceeded)", "reason", reason)
			p.sweeper.MarkSwept()
			continue
		}

		p.sweepOnce(ctx, wg)
		p.sweeper.MarkSwept()
	}
}

// sweepOnce runs one sweep cycle: plan eligible jobs and dispatch them.
func (p *Pipeline) sweepOnce(ctx context.Context, wg *sync.WaitGroup) {
	jobs := p.sweeper.Plan(ctx)
	if len(jobs) == 0 {
		slog.Info("sweep: no eligible tasks")
		return
	}

	slog.Info("sweep: dispatching", "jobs", len(jobs))
	p.notifier.Send(ctx, fmt.Sprintf("🧹 *Sweep started* — %d maintenance task(s)", len(jobs)))

	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}

		// Check budget before each task.
		if paused, _, _ := p.budget.IsPaused(); paused {
			slog.Info("sweep: stopping (paused)")
			return
		}
		if reason := p.budget.ExceededReason(); reason != "" {
			p.flagBudgetExceeded(reason)
			return
		}

		// Respect worker pool capacity.
		p.mu.Lock()
		if len(p.active) >= p.cfg.MaxConcurrent {
			p.mu.Unlock()
			slog.Info("sweep: at capacity, deferring remaining tasks")
			return
		}

		identifier := sweep.SweepIdentifier(job.RepoSlug, job.Task.BranchSuffix)
		if _, dupe := p.active[identifier]; dupe {
			p.mu.Unlock()
			continue
		}
		taskCtx, taskCancel := context.WithCancel(ctx)
		p.active[identifier] = struct{}{}
		p.activeRepos[identifier] = job.RepoSlug
		p.cancels[identifier] = taskCancel
		p.mu.Unlock()
		p.publishDashboardChange()

		wg.Add(1)
		go func(j sweep.Job, id string) {
			defer wg.Done()
			defer p.markDone(id)
			p.processSweepTask(taskCtx, j, id)
		}(job, identifier)
	}
}

// processSweepTask is one sweep task's full lifecycle: create worktree →
// run agent with task-specific prompt → check output → commit/push → PR.
func (p *Pipeline) processSweepTask(ctx context.Context, job sweep.Job, identifier string) {
	startedAt := time.Now()
	logger := slog.With("sweep_task", job.Task.Name, "repo", job.RepoSlug, "id", identifier)
	logger.Info("starting sweep task", "description", job.Task.Description)

	backend := p.agent

	if p.cfg.VerboseNotifications {
		p.notifier.Send(ctx, fmt.Sprintf("🧹 *Sweep: %s* on %s\n%s",
			notify.EscapeMarkdown(job.Task.Name),
			notify.EscapeMarkdown(job.RepoSlug),
			notify.EscapeMarkdown(job.Task.Description)))
	}

	branch := sweep.SweepBranchName(job.RepoSlug, job.Task.BranchSuffix)
	wt, err := repo.CreateWorktreeWithBranch(ctx, p.cfg.WorktreeBase, identifier, job.RepoPath, job.MainBranch, branch)
	if err != nil {
		logger.Error("worktree creation failed", "err", err)
		return
	}
	defer repo.CleanupWorktree(context.Background(), job.RepoPath, p.cfg.WorktreeBase, identifier)
	logger.Info("worktree created", "path", wt.Path, "branch", wt.Branch)

	logFile := filepath.Join(p.cfg.LogDir, identifier+".log")
	if err := agent.AttemptHeader(logFile); err != nil {
		logger.Warn("could not write attempt header", "err", err)
	}

	prompt := job.Task.Prompt(wt.Path)
	offset := agent.OffsetBefore(logFile)

	logger.Info("running agent",
		"backend", backend.Name(),
		"log", logFile,
		"timeout", p.cfg.AgentTimeout)

	runErr := backend.Run(ctx, agent.RunOptions{
		Workdir: wt.Path,
		Prompt:  prompt,
		LogFile: logFile,
		Timeout: p.cfg.AgentTimeout,
	})

	// Killed or shutdown — clean up without recording.
	if p.isKilled(identifier) {
		logger.Info("sweep task killed by user")
		return
	}
	if ctx.Err() != nil {
		logger.Info("sweep task cancelled (shutdown)")
		return
	}

	if errors.Is(runErr, agent.ErrTimedOut) {
		logger.Warn("sweep task timed out", "timeout", p.cfg.AgentTimeout)
		return
	}

	output := agent.ReadAfter(logFile, offset)

	usage := agent.ParseUsage(output)
	p.budget.Record(usage.TotalTokens, usage.CostUSD)
	p.recordUsage(usage, "sweep", identifier, "", backend)
	if usage.TotalTokens > 0 || usage.CostUSD > 0 {
		logger.Info("usage recorded", "tokens", usage.TotalTokens, "cost_usd", usage.CostUSD)
	}
	if reason := p.budget.ExceededReason(); reason != "" {
		p.flagBudgetExceeded(reason)
		p.notifier.Send(ctx, fmt.Sprintf(
			"⏸ *Daily budget exceeded*\n%s\nDispatching paused until next UTC midnight.",
			notify.EscapeMarkdown(reason)))
	}

	if rateLimited(backend, runErr, output) {
		logger.Warn("rate limit detected during sweep")
		p.flagRateLimit()
		return
	}

	if runErr != nil {
		logger.Warn("sweep agent exited with error", "err", runErr)
		// Record the run even on failure so cooldown is respected.
		if err := p.sweeper.RecordRun(job.RepoSlug, job.Task.Name); err != nil {
			logger.Warn("could not record sweep run in state", "err", err)
		}
		p.recordRun(state.RunHistory{
			Identifier: identifier, Repo: job.RepoSlug,
			AgentBackend: backend.Name(), RunType: "sweep",
			StartedAt: startedAt, FinishedAt: time.Now(), Status: "failed",
		})
		return
	}

	// BLOCKED — nothing to do for this task on this repo.
	if blocked := agent.BlockedLine(output); blocked != "" {
		logger.Info("sweep task blocked (nothing to do)", "reason", blocked)
		if err := p.sweeper.RecordRun(job.RepoSlug, job.Task.Name); err != nil {
			logger.Warn("could not record sweep run in state", "err", err)
		}
		p.recordRun(state.RunHistory{
			Identifier: identifier, Repo: job.RepoSlug,
			AgentBackend: backend.Name(), RunType: "sweep",
			StartedAt: startedAt, FinishedAt: time.Now(), Status: "blocked",
		})
		return
	}

	dirty, err := workingTreeChanged(ctx, wt.Path)
	if err != nil {
		logger.Error("git status failed", "err", err)
		return
	}
	committed, err := branchAhead(ctx, wt.Path, "origin/"+job.MainBranch)
	if err != nil {
		logger.Error("git rev-list failed", "err", err)
		return
	}
	if !dirty && !committed {
		logger.Info("sweep task made no changes")
		if err := p.sweeper.RecordRun(job.RepoSlug, job.Task.Name); err != nil {
			logger.Warn("could not record sweep run in state", "err", err)
		}
		p.recordRun(state.RunHistory{
			Identifier: identifier, Repo: job.RepoSlug,
			AgentBackend: backend.Name(), RunType: "sweep",
			StartedAt: startedAt, FinishedAt: time.Now(), Status: "no_change",
		})
		return
	}

	if err := runIn(ctx, wt.Path, "git", "add", "-A"); err != nil {
		logger.Error("git add failed", "err", err)
		return
	}

	commitMsg := appendCoAuthorTrailer(
		fmt.Sprintf("%s: %s\n\nAutonomous maintenance by Noctra using %s",
			job.Task.CommitPrefix, job.Task.Description, backend.Label()),
		backend.CoAuthor())

	staged, err := hasStagedChanges(ctx, wt.Path)
	if err != nil {
		logger.Error("git diff --cached failed", "err", err)
		return
	}
	if staged {
		if err := runIn(ctx, wt.Path, "git", "commit", "-m", commitMsg); err != nil {
			logger.Error("git commit failed", "err", err)
			return
		}
	}
	if err := runIn(ctx, wt.Path, "git", "push", "-u", "origin", wt.Branch); err != nil {
		logger.Error("git push failed", "err", err)
		return
	}
	logger.Info("pushed", "branch", wt.Branch)

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
			r, err := p.review.Review(ctx, job.Task.Name, job.Task.Description, diff)
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
					Workdir: wt.Path,
					Prompt:  fixPrompt,
					LogFile: logFile,
					Timeout: p.cfg.AgentTimeout,
				})

				if p.isKilled(identifier) {
					logger.Info("fix-pass killed by user")
					return
				}
				if ctx.Err() != nil {
					logger.Info("fix-pass cancelled (shutdown)")
					return
				}

				fixOutput := agent.ReadAfter(logFile, fixOffset)

				fixUsage := agent.ParseUsage(fixOutput)
				p.budget.Record(fixUsage.TotalTokens, fixUsage.CostUSD)
				p.recordUsage(fixUsage, "sweep", identifier, "", backend)
				if reason := p.budget.ExceededReason(); reason != "" {
					p.flagBudgetExceeded(reason)
					p.notifier.Send(ctx, fmt.Sprintf(
						"⏸ *Daily budget exceeded*\n%s\nDispatching paused until next UTC midnight.",
						notify.EscapeMarkdown(reason)))
				}

				switch classifyAgentRun(backend, fixErr, fixOutput) {
				case agentRunTimedOut:
					logger.Warn("fix-pass timed out", "timeout", p.cfg.AgentTimeout)
					return
				case agentRunRateLimited:
					logger.Warn("usage/rate limit detected during fix-pass")
					p.flagRateLimit()
					return
				case agentRunFailed:
					logger.Warn("fix-pass exited with error", "err", fixErr)
				}
				if err := runIn(ctx, wt.Path, "git", "add", "-A"); err != nil {
					logger.Error("git add failed after fix-pass", "err", err)
					return
				}
			}
		}
		if !reviewPassed {
			logger.Warn("gemini did not pass — creating PR with review comments attached",
				"attempts", reviewAttempts)
		}

		staged, err := hasStagedChanges(ctx, wt.Path)
		if err != nil {
			logger.Error("git diff --cached failed", "err", err)
			return
		}
		if staged {
			fixCommitMsg := appendCoAuthorTrailer(
				fmt.Sprintf("%s: Address review feedback\n\nAutonomous maintenance by Noctra using %s",
					job.Task.CommitPrefix, backend.Label()),
				backend.CoAuthor())
			if err := runIn(ctx, wt.Path, "git", "commit", "-m", fixCommitMsg); err != nil {
				logger.Error("git commit for review fixes failed", "err", err)
				return
			}
			if err := runIn(ctx, wt.Path, "git", "push", "origin", wt.Branch); err != nil {
				logger.Error("git push for review fixes failed", "err", err)
				return
			}
			logger.Info("pushed review fixes", "branch", wt.Branch)
		}
	}

	rawLog, _ := os.ReadFile(logFile)
	summary := agent.ExtractSummary(string(rawLog))

	reviewComment := ""
	if p.review.Enabled() {
		if reviewSkipped {
			reviewComment = fmt.Sprintf("⚠️ **Multi-model review:** Skipped (Gemini `%s` via `%s`)\n\n%s",
				p.review.Model, p.review.Mode, reviewBody)
		} else if reviewPassed {
			reviewComment = fmt.Sprintf("✅ **Multi-model review:** Passed (Gemini `%s` via `%s`)",
				p.review.Model, p.review.Mode)
		} else {
			reviewComment = fmt.Sprintf(
				"⚠️ **Multi-model review:** Did not pass after %d attempt(s). Please review before merging:\n\n<details>\n<summary>Gemini review comments</summary>\n\n```\n%s\n```\n\n</details>",
				reviewAttempts, reviewBody)
		}
	}

	prBody := fmt.Sprintf(
		"## 🧹 Maintenance: %s\n\n**Task:** %s\n**Repo:** %s\n\n## What was done\n\n%s\n\n---\n\n*Autonomous maintenance by [Noctra](https://github.com/ahmadAlMezaal/noctra) 🌙 using %s*\n%s",
		job.Task.Name, job.Task.Description, job.RepoSlug, summary, backend.Label(), github.NoctraPRBodyMarker)

	prTitle := fmt.Sprintf("%s: %s", job.Task.CommitPrefix, job.Task.Description)

	prURL, err := ghCreatePR(ctx, job.RepoPath, prTitle, prBody, job.MainBranch, wt.Branch)
	if err != nil {
		logger.Error("gh pr create failed", "err", err)
		return
	}

	if job.Task.PRLabel != "" {
		if err := ghAddLabel(ctx, job.RepoPath, prURL, job.Task.PRLabel); err != nil {
			logger.Warn("could not apply label", "label", job.Task.PRLabel, "err", err)
		}
	}

	logger.Info("✅ sweep PR created", "url", prURL)

	if reviewComment != "" {
		if err := p.gh.PostComment(ctx, prURL, reviewComment); err != nil {
			logger.Warn("could not post review verdict comment", "err", err)
		}
	}

	p.bumpSuccess()
	p.recordRun(state.RunHistory{
		Identifier: identifier, PRURL: prURL, Repo: job.RepoSlug,
		AgentBackend: backend.Name(), RunType: "sweep",
		StartedAt: startedAt, FinishedAt: time.Now(), Status: "pr_opened",
	})
	p.notifier.Send(ctx, fmt.Sprintf("✅ *Sweep: %s* on %s\nPR: %s",
		notify.EscapeMarkdown(job.Task.Name),
		notify.EscapeMarkdown(job.RepoSlug),
		prURL))

	if err := p.sweeper.RecordRun(job.RepoSlug, job.Task.Name); err != nil {
		logger.Warn("could not record sweep run in state", "err", err)
	}

	if p.store != nil {
		if err := p.store.Update(prURL, func(r *state.PRState) {
			r.TicketID = identifier
			r.AgentBackend = backend.Name()
		}); err != nil {
			logger.Warn("could not persist sweep PR in state", "err", err)
		}
	}
}
