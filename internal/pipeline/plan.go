package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/source"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

// needsPlanConfirm reports whether a ticket should go through the plan-confirm
// flow. True when either the global PLAN_CONFIRM=true is set, or the issue
// carries the plan-confirm label (e.g. "plan-first").
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

// hasPendingPlan reports whether a ticket has a plan awaiting approval.
// Caller must NOT hold p.mu (the store has its own locking).
func (p *Pipeline) hasPendingPlan(identifier string) bool {
	if p.store == nil {
		return false
	}
	ps := p.store.GetPlan(identifier)
	return ps.Plan != ""
}

// pollPlanApprovals checks all pending plans for human approval comments and
// dispatches approved tickets for implementation. Called at the start of each
// poll cycle. available is decremented for each dispatched ticket.
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

		// Fetch the full ticket so we have all fields for the implementation
		// prompt (title, description, comments, labels, project).
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ticketSrc := p.sourceByName(ps.Source)
		issue, err := ticketSrc.FetchByIdentifier(fctx, identifier)
		cancel()
		if err != nil {
			slog.Warn("could not fetch ticket for approved plan", "id", identifier, "source", ps.Source, "err", err)
			continue
		}

		// Re-fetch comments for the clarification list — GetIssueByIdentifier
		// doesn't include them.
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

		// Remove the plan-confirm label if it's a per-ticket label.
		if issue.HasLabel(p.cfg.PlanConfirmLabel) {
			if err := ticketSrc.RemovePlanLabel(ctx, issue); err != nil {
				slog.Warn("could not remove plan-confirm label", "id", identifier, "err", err)
			}
		}

		// Delete the plan from state — it's been approved and we've
		// committed to dispatching the implementation.
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

// checkPlanApproval fetches comments on the issue and checks whether any
// non-system comment posted after the plan constitutes approval.
func (p *Pipeline) checkPlanApproval(ctx context.Context, ps state.PlanState) (bool, error) {
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticketSrc := p.sourceByName(ps.Source)
	ticket := source.Ticket{Source: ticketSrc.Name(), ID: ps.IssueID}
	comments, err := ticketSrc.FetchComments(fctx, ticket)
	if err != nil {
		return false, err
	}

	// Comments are returned in chronological order (last: 50). We iterate
	// backwards to find the latest plan comment and check if any approval
	// comment was posted after it. This avoids false-positive approvals
	// from older plan attempts on re-planned tickets.
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

// isPlanComment reports whether a comment body is the plan-confirm comment
// Noctra posted.
func isPlanComment(body string) bool {
	return len(body) >= len(source.PlanConfirmCommentPrefix) &&
		body[:len(source.PlanConfirmCommentPrefix)] == source.PlanConfirmCommentPrefix
}

// processPlanOnly runs the agent in plan-only mode, posts the plan as a source
// comment, and records the pending plan in the state store. The ticket stays
// in its current state (trigger state / has trigger label) so the next poll
// cycle can check for approval.
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

	// ── Resolve target repo (same logic as process) ──────────────────────────
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

	// Create a worktree so the agent can read the codebase.
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
		Workdir: wt.Path,
		Prompt:  prompt,
		LogFile: logFile,
		Timeout: p.cfg.AgentTimeout,
	})

	// Check early exits.
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

	// Record usage from the plan pass (ENG-217).
	p.budget.Record(usage.TotalTokens, usage.CostUSD)
	p.recordUsage(usage, "plan", id, "", backend)
	if reason := p.budget.ExceededReason(); reason != "" {
		p.flagBudgetExceeded(reason)
	}

	// Always clean up the worktree — the plan pass is read-only.
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
		// Agent didn't produce a plan between markers — fall back to the
		// full output as the plan (best effort).
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

	// Post the plan as a Linear comment.
	planComment := fmt.Sprintf(
		"%s\n\n%s\n\n---\n\nReply with **go**, **lgtm**, or **👍** to approve and start implementation.",
		source.PlanConfirmCommentPrefix, plan)

	if err := p.ticketComment(ctx, issue, planComment); err != nil {
		logger.Warn("could not post plan comment", "err", err)
	}

	// Save the plan to the state store.
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

// processWithPlan runs the full ticket implementation using the approved plan
// as context. This is the same as process() but uses BuildPlanImplementPrompt
// instead of BuildPrompt.
func (p *Pipeline) processWithPlan(ctx context.Context, issue source.Ticket, plan string) {
	id := issue.Identifier
	logger := slog.With("id", id)
	logger.Info("starting implementation with approved plan", "title", issue.Title)

	// Store the plan in the pipeline map so process() can read it and use the
	// plan-aware prompt builder.
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
