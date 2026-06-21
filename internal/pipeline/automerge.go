package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
)

// runAutoMerger is the auto-merge-on-done loop that runs alongside the Linear-poll
// loop. Started by Run when cfg.AutoMergeOnDone is true and the store initialized
// without error.
func (p *Pipeline) runAutoMerger(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	slog.Info("auto-merge scheduler starting",
		"interval", p.cfg.MergePollInterval,
		"done_state", p.cfg.DoneState,
		"merge_method", p.cfg.MergeMethod,
		"delete_branch", p.cfg.DeleteBranchAfterMerge,
	)

	ticker := time.NewTicker(p.cfg.MergePollInterval)
	defer ticker.Stop()

	// Initial scan after a brief delay so the Linear-startup output clears.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	p.autoMergePollOnce(ctx, wg)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.autoMergePollOnce(ctx, wg)
		}
	}
}

// autoMergePollOnce scans all tracked tickets for ones in the done state,
// fetches their PRs, and attempts to merge if checks pass.
func (p *Pipeline) autoMergePollOnce(ctx context.Context, wg *sync.WaitGroup) {
	// Fetch all tracked PRs from the state store
	prStates := p.store.All()
	if len(prStates) == 0 {
		return
	}

	// Dedupe ticket IDs from PR states
	ticketIDs := make(map[string]struct{})
	for _, pr := range prStates {
		if pr.TicketID != "" {
			ticketIDs[pr.TicketID] = struct{}{}
		}
	}

	if len(ticketIDs) == 0 {
		return
	}

	// Check each tracked ticket to see if it's in the done state
	for ticketID := range ticketIDs {
		// Avoid spawning goroutines; check synchronously to avoid too many concurrent ops
		p.checkAndMergeTicket(ctx, ticketID)
	}
}

// checkAndMergeTicket checks if a ticket is in the done state and merges
// its associated PR if all checks pass.
func (p *Pipeline) checkAndMergeTicket(ctx context.Context, ticketID string) {
	// Fetch the ticket to check its current state
	issue, err := p.linear.GetIssueByIdentifier(ctx, ticketID)
	if err != nil {
		slog.Warn("auto-merge: fetch ticket failed", "ticket_id", ticketID, "err", err)
		return
	}

	// Check if ticket is in the done state
	if issue.StateName() != p.cfg.DoneState {
		return
	}

	// Ticket is in done state; find its PR(s) and merge
	prURLs := p.store.AllByTicketID(ticketID)
	if len(prURLs) == 0 {
		slog.Debug("auto-merge: no PR found for ticket in done state", "ticket_id", ticketID)
		return
	}

	for _, prURL := range prURLs {
		p.mergePRForDoneTicket(ctx, ticketID, issue.ID, prURL)
	}
}

// mergePRForDoneTicket attempts to merge a PR for a ticket that's now in the
// done state. On failure, it posts a comment explaining why the merge failed.
func (p *Pipeline) mergePRForDoneTicket(ctx context.Context, ticketID, issueID, prURL string) {
	gh := github.New()
	opts := github.MergePROptions{
		Method:       p.cfg.MergeMethod,
		DeleteBranch: p.cfg.DeleteBranchAfterMerge,
	}

	result := gh.MergePR(ctx, prURL, opts)

	if result.Success && result.Merged {
		slog.Info("auto-merge: PR merged", "ticket_id", ticketID, "pr_url", prURL, "message", result.Message)
		p.notifier.Send(ctx, fmt.Sprintf(
			"✅ *Auto-merged* %s\n%s\n%s",
			ticketID,
			notify.EscapeMarkdown(result.Message),
			prURL,
		))
		return
	}

	// Merge failed; comment on the ticket explaining why
	slog.Warn("auto-merge: merge failed", "ticket_id", ticketID, "pr_url", prURL, "message", result.Message)
	p.notifier.Send(ctx, fmt.Sprintf(
		"⚠️ *Could not auto-merge* %s\n%s\n%s",
		ticketID,
		notify.EscapeMarkdown(result.Message),
		prURL,
	))

	// Post a comment on Linear explaining the merge failure
	commentBody := fmt.Sprintf(
		"**Noctra: Cannot auto-merge PR**\n\n%s\n\n%s",
		result.Message,
		prURL,
	)
	if err := p.linear.Comment(ctx, issueID, commentBody); err != nil {
		slog.Warn("auto-merge: failed to comment on issue", "ticket_id", ticketID, "err", err)
	}
}
