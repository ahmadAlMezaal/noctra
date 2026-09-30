package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/onelastcommit/noctra/internal/agent"
	"github.com/onelastcommit/noctra/internal/notify"
	"github.com/onelastcommit/noctra/internal/repo"
	"github.com/onelastcommit/noctra/internal/source"
	"github.com/onelastcommit/noctra/internal/state"
)

func (p *Pipeline) needsPlanConfirm(issue source.Ticket) bool {
	if p.store == nil {
		return false
	}
	if p.cfg.PlanConfirm {
		return true
	}
	if p.cfg.PlanConfirmLabel != "" && issue.HasLabel(p.cfg.PlanConfirmLabel) {
		return true
	}
	return false
}

func (p *Pipeline) hasPendingPlan(identifier string) bool {
	if p.store == nil {
		return false
	}
	ps := p.store.GetPlan(identifier)
	return ps.Plan != ""
}

func (p *Pipeline) pollPlanApprovals(ctx context.Context, wg *sync.WaitGroup, available *int) {
	if p.store == nil {
		return
	}
	plans := p.store.AllPlans()
	if len(plans) == 0 {
		return
	}

	slog.Debug("checking plan approvals", "pending", len(plans))

	for identifier, ps := range plans {
		if *available <= 0 {
			break
		}
		p.mu.Lock()
		if dispatchCapReached(p.cfg.MaxDispatches, p.totalDispatches) {
			p.mu.Unlock()
			return
		}
		if _, dupe := p.active[identifier]; dupe {
			p.mu.Unlock()
			continue
		}
		p.mu.Unlock()

		approved, err := p.checkPlanApproval(ctx, ps)
		if err != nil {
			slog.Warn("plan approval check failed", "id", identifier, "err", err)
			continue
		}
		if !approved {
			continue
		}

		slog.Info("plan approved — resuming implementation", "id", identifier)

		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ticketSrc := p.sourceByName(ps.Source)
		issue, err := ticketSrc.FetchByIdentifier(fctx, identifier)
		cancel()
		if err != nil {
			slog.Warn("could not fetch ticket for approved plan", "id", identifier, "source", ps.Source, "err", err)
			continue
		}

		cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
		comments, cerr := ticketSrc.FetchComments(cctx, issue)
		ccancel()
		if cerr == nil {
			issue.Comments = comments
		}

		p.mu.Lock()
		if _, dupe := p.active[identifier]; dupe {
			p.mu.Unlock()
			continue
		}
		ticketCtx, ticketCancel := context.WithCancel(ctx)
		p.active[identifier] = struct{}{}
		p.activeMeta[identifier] = activeRunMeta{runType: "plan", startedAt: time.Now()}
		p.cancels[identifier] = ticketCancel
		p.totalDispatches++
		p.mu.Unlock()
		p.publishDashboardChange()

		if issue.HasLabel(p.cfg.PlanConfirmLabel) {
			if err := ticketSrc.RemovePlanLabel(ctx, issue); err != nil {
				slog.Warn("could not remove plan-confirm label", "id", identifier, "err", err)
			}
		}

		plan := ps.Plan
		if err := p.store.DeletePlan(identifier); err != nil {
			slog.Warn("could not delete plan state", "id", identifier, "err", err)
		}

		*available--

		wg.Add(1)
		go func(iss source.Ticket, approvedPlan string) {
			defer wg.Done()
			defer p.markDone(iss.Identifier)
			p.processWithPlan(ticketCtx, iss, approvedPlan)
		}(issue, plan)
	}
}

func (p *Pipeline) checkPlanApproval(ctx context.Context, ps state.PlanState) (bool, error) {
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticketSrc := p.sourceByName(ps.Source)
	ticket := source.Ticket{Source: ticketSrc.Name(), ID: ps.IssueID}
	comments, err := ticketSrc.FetchComments(fctx, ticket)
	if err != nil {
		return false, err
	}

	hasApprovalAfterPlan := false
	for i := len(comments) - 1; i >= 0; i-- {
		body := comments[i].Body
		if source.IsSystemComment(body) && isPlanComment(body) {
			return hasApprovalAfterPlan, nil
		}
		if source.IsSystemComment(body) {
			continue
		}
		if source.IsApprovalComment(body) {
			hasApprovalAfterPlan = true
		}
	}
	return false, nil
}

func isPlanComment(body string) bool {
	return len(body) >= len(source.PlanConfirmCommentPrefix) &&
		body[:len(source.PlanConfirmCommentPrefix)] == source.PlanConfirmCommentPrefix
}

func (p *Pipeline) processPlanOnly(ctx context.Context, issue source.Ticket) {
	id := issue.Identifier
	logger := slog.With("id", id)
	logger.Info("running plan-only pass")

	backend := p.resolveBackend(issue)

	if p.cfg.VerboseNotifications {
		p.notifier.Send(ctx, fmt.Sprintf("📋 *%s* — %s\nRunning plan-only pass.",
			id, notify.EscapeMarkdown(issue.Title)))
	}

	logFile := filepath.Join(p.cfg.LogDir, id+".log")
	if err := agent.AttemptHeader(logFile); err != nil {
		logger.Warn("could not write attempt header", "err", err)
	}

	var (
		resolved repo.Resolved
		err      error
	)
	if issue.RepoRef != "" {
		resolved, err = p.resolver.ResolveDirect(ctx, issue.RepoRef, issue.RepoBranch)
	} else {
		resolved, err = p.resolver.Resolve(ctx, issue.ProjectName)
	}
	if err != nil {
		logger.Error("repo resolution failed (plan)", "err", err)
		var nte *repo.NonTransientError
		if errors.As(err, &nte) {
			p.skipPermanently(id)
			if cerr := p.ticketComment(ctx, issue,
				fmt.Sprintf("❌ **Noctra: No repo for this ticket**\n\n%s\n\nAdd a `Repo: owner/name` directive to this ticket's source metadata.",
					err.Error())); cerr != nil {
				slog.Warn("ticket comment failed", "issue_id", issue.ID, "err", cerr)
			}
			return
		}
		attempts := p.bumpFailed(id)
		p.ticketBackToTrigger(ctx, issue, fmt.Sprintf(
			"❌ **Noctra: repo resolution failed** (attempt %d/%d)\n\n%s",
			attempts, p.cfg.MaxRetries, err.Error()))
		return
	}

	wt, err := repo.CreateWorktree(ctx, p.cfg.WorktreeBase, id, resolved.Path, resolved.MainBranch)
	if err != nil {
		logger.Error("worktree creation failed (plan)", "err", err)
		p.ticketBackToTrigger(ctx, issue, fmt.Sprintf(
			"❌ **Noctra: Setup failed**\n\nCould not create worktree for planning pass.\n\nTicket moved back to **%s**.",
			p.cfg.TriggerState))
		return
	}

	prompt := agent.BuildPlanPrompt(agent.BuildPromptInput{
		Identifier:  id,
		Title:       issue.Title,
		Description: issue.Description,
		Comments:    issue.ClarificationComments(),
	})

	offset := agent.OffsetBefore(logFile)

	usage, runErr := backend.Run(ctx, agent.RunOptions{
		Workdir:   wt.Path,
		Prompt:    prompt,
		LogFile:   logFile,
		Timeout:   p.cfg.AgentTimeout,
		MaxTokens: p.cfg.AgentMaxTokens,
	})

	if p.isKilled(id) {
		logger.Info("plan-only run killed by user")
		repo.CleanupWorktree(context.Background(), resolved.Path, p.cfg.WorktreeBase, id)
		return
	}
	if ctx.Err() != nil {
		logger.Info("plan-only run cancelled (shutdown)")
		repo.CleanupWorktree(context.Background(), resolved.Path, p.cfg.WorktreeBase, id)
		return
	}

	output := agent.ReadAfter(logFile, offset)

	p.budget.Record(usage.TotalTokens, usage.CostUSD)
	p.recordUsage(usage, "plan", id, "", backend)
	if reason := p.budget.ExceededReason(); reason != "" {
		p.flagBudgetExceeded(reason)
	}

	repo.CleanupWorktree(ctx, resolved.Path, p.cfg.WorktreeBase, id)

	if errors.Is(runErr, agent.ErrTimedOut) {
		p.bumpFailed(id)
		p.ticketBackToTrigger(ctx, issue, fmt.Sprintf(
			"⏰ **Noctra: Plan generation timed out**\n\nTicket moved back to **%s**.",
			p.cfg.TriggerState))
		return
	}

	if runErr != nil {
		attempts := p.bumpFailed(id)
		logger.Warn("plan-only agent failed", "err", runErr, "attempt", attempts)
		p.ticketBackToTrigger(ctx, issue, fmt.Sprintf(
			"❌ **Noctra: Plan generation failed** (attempt %d/%d)\n\nThe agent failed to produce a plan. Will retry on next poll cycle.\n\nTicket moved back to **%s**.",
			attempts, p.cfg.MaxRetries, p.cfg.TriggerState))
		return
	}

	plan, ok := agent.ExtractPlan(output)
	if !ok {
		plan = agent.ExtractSummary(output)
		if plan == "" {
			attempts := p.bumpFailed(id)
			logger.Warn("plan-only agent produced no plan", "attempt", attempts)
			p.ticketBackToTrigger(ctx, issue, fmt.Sprintf(
				"❌ **Noctra: No plan produced** (attempt %d/%d)\n\nThe agent completed but did not produce an implementation plan.\n\nTicket moved back to **%s**.",
				attempts, p.cfg.MaxRetries, p.cfg.TriggerState))
			return
		}
	}
	logger.Info("plan produced", "plan_length", len(plan))

	planComment := fmt.Sprintf(
		"%s\n\n%s\n\n---\n\nReply with **go**, **lgtm**, or **👍** to approve and start implementation.",
		source.PlanConfirmCommentPrefix, plan)

	if err := p.ticketComment(ctx, issue, planComment); err != nil {
		logger.Warn("could not post plan comment", "err", err)
	}

	if p.store != nil {
		if err := p.store.SavePlan(id, state.PlanState{
			Source:    issue.Source,
			IssueID:   issue.ID,
			Plan:      plan,
			PlannedAt: time.Now(),
		}); err != nil {
			logger.Warn("could not save plan state", "err", err)
		}
	}

	logger.Info("plan posted — awaiting approval", "id", id)
	p.notifier.Send(ctx, fmt.Sprintf("📋 *%s* — %s\nPlan posted. Waiting for human approval.",
		id, notify.EscapeMarkdown(issue.Title)))
}

func (p *Pipeline) processWithPlan(ctx context.Context, issue source.Ticket, plan string) {
	id := issue.Identifier
	logger := slog.With("id", id)
	logger.Info("starting implementation with approved plan", "title", issue.Title)

	p.mu.Lock()
	if p.approvedPlans == nil {
		p.approvedPlans = map[string]string{}
	}
	p.approvedPlans[id] = plan
	p.mu.Unlock()

	p.process(ctx, issue)

	p.mu.Lock()
	delete(p.approvedPlans, id)
	p.mu.Unlock()
}

func (p *Pipeline) sourceByName(name string) source.TicketSource {
	if name == "" {
		name = "linear"
	}
	for _, src := range p.sources {
		if src.Name() == name {
			return src
		}
	}
	return p.sources[0]
}
