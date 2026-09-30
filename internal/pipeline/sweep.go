package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onelastcommit/noctra/internal/agent"
	"github.com/onelastcommit/noctra/internal/budget"
	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/github"
	"github.com/onelastcommit/noctra/internal/notify"
	"github.com/onelastcommit/noctra/internal/repo"
	"github.com/onelastcommit/noctra/internal/review"
	"github.com/onelastcommit/noctra/internal/state"
	"github.com/onelastcommit/noctra/internal/sweep"
)

func (p *Pipeline) runSweepLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	slog.Info("sweep scheduler starting",
		"interval", p.cfg.SweepInterval,
		"max_tasks", p.cfg.SweepMaxTasks,
	)

	for {
		due := p.sweeper.DueIn()
		var manual *sweep.PlanOptions
		if due > 0 {
			slog.Debug("sweep: next sweep in", "due_in", due)
			timer := time.NewTimer(due)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case req := <-p.sweepNow:
				timer.Stop()
				manual = &req
			}
		}

		if ctx.Err() != nil {
			return
		}

		if paused, until, reason := p.budget.IsPaused(); paused {
			if manual != nil {
				p.refuseManualSweep(ctx, "budget paused: "+reason)
				continue
			}
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
			if manual != nil {
				p.refuseManualSweep(ctx, "budget exceeded: "+reason)
				continue
			}
			slog.Debug("sweep: skipping (budget exceeded)", "reason", reason)
			p.sweeper.MarkSwept()
			continue
		}

		if manual != nil {
			slog.Info("sweep: manual trigger", "tasks", manual.Tasks, "repos", manual.Repos, "forced", manual.IgnoreCooldown)
			p.sweepOnce(ctx, wg, *manual)
			continue
		}

		p.sweepOnce(ctx, wg, sweep.PlanOptions{})
		p.sweeper.MarkSwept()
	}
}

func (p *Pipeline) TriggerSweep(opts sweep.PlanOptions) error {
	if p.sweeper == nil {
		return errors.New("sweeps are disabled — set SWEEP_ENABLED=true")
	}
	select {
	case p.sweepNow <- opts:
		return nil
	default:
		return errors.New("a manual sweep is already queued")
	}
}

func (p *Pipeline) SweepTaskNames() []string {
	if p.sweeper == nil {
		return nil
	}
	return p.sweeper.TaskNames()
}

func (p *Pipeline) refuseManualSweep(ctx context.Context, reason string) {
	slog.Warn("sweep: manual trigger refused", "reason", reason)
	p.notifier.Send(ctx, fmt.Sprintf("⏸ *Manual sweep refused* — %s", notify.EscapeMarkdown(reason)))
}

func (p *Pipeline) branchFromLinearDirective(ctx context.Context, ref string) string {
	want, err := github.ExtractOwnerRepo(ref)
	if err != nil {
		return ""
	}
	projects, err := p.linear.ListProjects(ctx)
	if err != nil {
		slog.Warn("sweep: could not list Linear projects for branch directive", "err", err)
		return ""
	}
	for i := range projects {
		r, b := projects[i].RepoDirective()
		if r == "" || b == "" {
			continue
		}
		if got, err := github.ExtractOwnerRepo(r); err == nil && strings.EqualFold(got, want) {
			return b
		}
	}
	return ""
}

func (p *Pipeline) sweepOnce(ctx context.Context, wg *sync.WaitGroup, opts sweep.PlanOptions) {
	jobs := p.sweeper.PlanWith(ctx, opts)
	if len(jobs) == 0 {
		slog.Info("sweep: no eligible tasks")
		if opts.Tasks != nil || opts.Repos != nil || opts.IgnoreCooldown {
			p.notifier.Send(ctx, "🧹 *Manual sweep* — no eligible tasks matched")
		}
		return
	}

	slog.Info("sweep: dispatching", "jobs", len(jobs))
	p.notifier.Send(ctx, fmt.Sprintf("🧹 *Sweep started* — %d maintenance task(s)", len(jobs)))

	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}

		if paused, _, _ := p.budget.IsPaused(); paused {
			slog.Info("sweep: stopping (paused)")
			return
		}
		if reason := p.budget.ExceededReason(); reason != "" {
			p.flagBudgetExceeded(reason)
			return
		}

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
		p.activeMeta[identifier] = activeRunMeta{runType: "sweep", startedAt: time.Now()}
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

type sweepAbort struct {
	job        sweep.Job
	identifier string
	backend    agent.Backend
	startedAt  time.Time
	usage      agent.Usage
	logger     *slog.Logger
	detail     string
	worktree   repo.Worktree
}

func (p *Pipeline) abortSweepTask(ctx context.Context, a sweepAbort) {
	prURL := p.salvageAbortedWork(ctx, a)

	if err := p.sweeper.RecordRun(a.job.RepoSlug, a.job.Task.Name); err != nil {
		a.logger.Warn("could not record sweep run in state", "err", err)
	}
	p.recordRun(state.RunHistory{
		Identifier: a.identifier, PRURL: prURL, Repo: a.job.RepoSlug,
		AgentBackend: a.backend.Name(), RunType: "sweep",
		StartedAt: a.startedAt, FinishedAt: time.Now(), Status: "aborted",
	})

	detail := a.detail
	if prURL != "" {
		detail += "\nSalvaged the work so far as a draft PR: " + prURL
	}
	p.notifySweepOutcome(ctx, a.job, "🛑", "aborted", detail, a.usage)
}

func (p *Pipeline) salvageAbortedWork(ctx context.Context, a sweepAbort) string {
	dirty, err := workingTreeChanged(ctx, a.worktree.Path)
	if err != nil {
		a.logger.Warn("could not check worktree for salvageable work", "err", err)
		return ""
	}
	committed, err := branchAhead(ctx, a.worktree.Path, "origin/"+a.job.MainBranch)
	if err != nil {
		a.logger.Warn("could not check branch for salvageable work", "err", err)
		return ""
	}
	if !dirty && !committed {
		a.logger.Info("aborted sweep left no changes to salvage")
		return ""
	}

	if dirty {
		if err := runIn(ctx, a.worktree.Path, "git", "add", "-A"); err != nil {
			a.logger.Warn("could not stage salvaged work", "err", err)
			return ""
		}
		staged, err := hasStagedChanges(ctx, a.worktree.Path)
		if err != nil {
			a.logger.Warn("could not inspect staged salvaged work", "err", err)
			return ""
		}
		if staged {
			msg := appendCoAuthorTrailer(
				fmt.Sprintf("%s: %s (partial)\n\nAutonomous maintenance by Noctra using %s.\n%s",
					a.job.Task.CommitPrefix, a.job.Task.Description, a.backend.Label(), a.detail),
				a.backend.CoAuthor())
			if err := runIn(ctx, a.worktree.Path, "git", "commit", "-m", msg); err != nil {
				a.logger.Warn("could not commit salvaged work", "err", err)
				return ""
			}
		}
	}

	if err := pushSweepBranch(ctx, a.worktree.Path, a.worktree.Branch); err != nil {
		a.logger.Warn("could not push salvaged work", "err", err)
		return ""
	}

	stat := gitDiffStat(ctx, a.worktree.Path, "origin/"+a.job.MainBranch)
	prURL, err := ghCreateDraftPR(ctx, a.job.RepoPath,
		salvagedPRTitle(a.job.Task.CommitPrefix, a.job.Task.Description),
		salvagedPRBody(a.job, a.detail, stat, a.backend.Label()),
		a.job.MainBranch, a.worktree.Branch)
	if err != nil {
		a.logger.Warn("could not open draft PR for salvaged work", "err", err)
		return ""
	}

	if a.job.Task.PRLabel != "" {
		if err := ghAddLabel(ctx, a.job.RepoPath, prURL, a.job.Task.PRLabel); err != nil {
			a.logger.Warn("could not apply label", "label", a.job.Task.PRLabel, "err", err)
		}
	}

	a.logger.Info("salvaged aborted sweep work into a draft PR", "url", prURL)
	p.recordSweepPR(ctx, prURL, a.identifier, a.backend, a.worktree.Path, a.logger)
	return prURL
}

func (p *Pipeline) recordSweepPR(ctx context.Context, prURL, identifier string, backend agent.Backend, workdir string, logger *slog.Logger) {
	if p.store == nil {
		return
	}
	headSHA := gitHead(ctx, workdir)
	if err := p.store.Update(prURL, func(r *state.PRState) {
		r.TicketID = identifier
		r.AgentBackend = backend.Name()
		if headSHA != "" {
			r.LastPushedSHA = headSHA
		}
	}); err != nil {
		logger.Warn("could not persist sweep PR in state", "err", err)
	}
}

func (p *Pipeline) repoLessons(repoSlug string) string {
	if p.store == nil {
		return ""
	}
	lessons, err := p.store.GetLessons(repoSlug)
	if err != nil {
		slog.Warn("could not load repo lessons", "repo", repoSlug, "err", err)
		return ""
	}
	return lessons
}

func salvagedPRTitle(commitPrefix, description string) string {
	return fmt.Sprintf("%s: %s (partial)", commitPrefix, description)
}

func salvagedPRBody(job sweep.Job, detail, diffStat, backendLabel string) string {
	stat := diffStat
	if stat == "" {
		stat = "(diffstat unavailable)"
	}
	return fmt.Sprintf(`## 🛑 Partial maintenance: %s

**This run was cut short and the diff is UNVERIFIED.** %s

It is opened as a draft so the work is not thrown away. The agent never reached its
own build/test verification step, so nothing here is known to be green — review it,
or close it, before merging.

**Task:** %s
**Repo:** %s

## Changes on the branch

`+"```"+`
%s
`+"```"+`

---

*Autonomous maintenance by [Noctra](https://github.com/onelastcommit/noctra) 🌙 using %s*
%s`, job.Task.Name, detail, job.Task.Description, job.RepoSlug, stat, backendLabel,
		github.NoctraPRBodyMarker)
}

func (p *Pipeline) notifySweepOutcome(ctx context.Context, job sweep.Job,
	icon, outcome, detail string, usage agent.Usage) {

	msg := fmt.Sprintf("%s *Sweep %s* — %s on %s", icon, outcome,
		notify.EscapeMarkdown(job.Task.Name), notify.EscapeMarkdown(job.RepoSlug))
	if detail != "" {
		msg += "\n" + notify.EscapeMarkdown(truncateDetail(detail))
	}
	if usage.TotalTokens > 0 {
		msg += fmt.Sprintf("\n%s tokens", budget.FormatTokens(usage.TotalTokens))
		if usage.CostUSD > 0 {
			msg += fmt.Sprintf(" · ~$%.2f", usage.CostUSD)
		}
	}
	p.notifier.Send(context.WithoutCancel(ctx), msg)
}

func truncateDetail(s string) string {
	const maxDetail = 300
	if len(s) <= maxDetail {
		return s
	}
	return strings.TrimSpace(s[:maxDetail]) + "…"
}

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
	openPR, err := ghOpenPRForBranch(ctx, job.RepoPath, branch)
	if err != nil {
		logger.Warn("could not check for an open sweep PR, skipping", "branch", branch, "err", err)
		return
	}
	if openPR != "" {
		logger.Info("previous sweep PR still open, skipping", "url", openPR)
		return
	}

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

	prompt := job.Task.Prompt(wt.Path) + agent.RepoLessonsSection(p.repoLessons(job.RepoSlug))
	offset := agent.OffsetBefore(logFile)

	sweepMaxTokens := p.cfg.AgentMaxTokens
	if sweepMaxTokens <= 0 {
		sweepMaxTokens = config.DefaultSweepMaxTokens
	}

	logger.Info("running agent",
		"backend", backend.Name(),
		"log", logFile,
		"timeout", p.cfg.SweepTimeout,
		"max_tokens", sweepMaxTokens)

	usage, runErr := backend.Run(ctx, agent.RunOptions{
		Workdir:   wt.Path,
		Prompt:    prompt,
		LogFile:   logFile,
		Timeout:   p.cfg.SweepTimeout,
		MaxTokens: sweepMaxTokens,
	})

	if p.isKilled(identifier) {
		logger.Info("sweep task killed by user")
		return
	}
	if ctx.Err() != nil {
		logger.Info("sweep task cancelled (shutdown)")
		return
	}

	if errors.Is(runErr, agent.ErrTimedOut) {
		p.budget.Record(usage.TotalTokens, usage.CostUSD)
		p.recordUsage(usage, "sweep", identifier, "", backend)
		logger.Warn("sweep task timed out",
			"timeout", p.cfg.SweepTimeout, "tokens", usage.TotalTokens)
		p.abortSweepTask(ctx, sweepAbort{
			job: job, identifier: identifier, backend: backend, startedAt: startedAt,
			usage: usage, logger: logger, worktree: wt,
			detail: fmt.Sprintf("Ran out of time after %s without finishing.", p.cfg.SweepTimeout),
		})
		return
	}

	if errors.Is(runErr, agent.ErrTokenCapExceeded) {
		p.budget.Record(usage.TotalTokens, usage.CostUSD)
		p.recordUsage(usage, "sweep", identifier, "", backend)
		logger.Warn("sweep task aborted: per-run token ceiling reached",
			"max_tokens", sweepMaxTokens, "tokens", usage.TotalTokens)
		p.abortSweepTask(ctx, sweepAbort{
			job: job, identifier: identifier, backend: backend, startedAt: startedAt,
			usage: usage, logger: logger, worktree: wt,
			detail: fmt.Sprintf("Hit the %s token ceiling without finishing.",
				budget.FormatTokens(int64(sweepMaxTokens))),
		})
		return
	}

	output := agent.ReadAfter(logFile, offset)

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
		if err := p.sweeper.RecordRun(job.RepoSlug, job.Task.Name); err != nil {
			logger.Warn("could not record sweep run in state", "err", err)
		}
		p.recordRun(state.RunHistory{
			Identifier: identifier, Repo: job.RepoSlug,
			AgentBackend: backend.Name(), RunType: "sweep",
			StartedAt: startedAt, FinishedAt: time.Now(), Status: "failed",
		})
		p.notifySweepOutcome(ctx, job, "❌", "failed", runErr.Error(), usage)
		return
	}

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
		p.notifySweepOutcome(ctx, job, "🚧", "nothing to do", blocked, usage)
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
		p.notifySweepOutcome(ctx, job, "🔕", "no changes", "The agent finished without touching any files.", usage)
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
	if err := pushSweepBranch(ctx, wt.Path, wt.Branch); err != nil {
		logger.Error("git push failed", "err", err)
		return
	}
	logger.Info("pushed", "branch", wt.Branch)

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
				fixUsage, fixErr := backend.Run(ctx, agent.RunOptions{
					Workdir:   wt.Path,
					Prompt:    fixPrompt,
					LogFile:   logFile,
					Timeout:   p.cfg.SweepTimeout,
					MaxTokens: sweepMaxTokens,
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
					logger.Warn("fix-pass timed out", "timeout", p.cfg.SweepTimeout)
					return
				case agentRunTokenCapped:
					logger.Warn("fix-pass aborted: per-run token ceiling reached", "max_tokens", sweepMaxTokens)
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
		"## 🧹 Maintenance: %s\n\n**Task:** %s\n**Repo:** %s\n\n## What was done\n\n%s\n\n---\n\n*Autonomous maintenance by [Noctra](https://github.com/onelastcommit/noctra) 🌙 using %s*\n%s",
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
	p.recordSweepPR(ctx, prURL, identifier, backend, wt.Path, logger)

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
