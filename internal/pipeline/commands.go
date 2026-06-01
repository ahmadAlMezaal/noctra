package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/notify"
	"github.com/ahmadAlMezaal/nightshift/internal/telegram"
)

// registerCommands wires the Telegram command handlers that require a running
// pipeline (status, kill, requeue). Called from Run when Telegram is enabled.
func (p *Pipeline) registerCommands(d *telegram.Dispatcher) {
	d.Register("status", "Show active runs and session stats", p.handleStatus)
	d.Register("kill", "Kill a running ticket (e.g. /kill ENG-42)", p.handleKill)
	d.Register("requeue", "Re-queue a ticket with context (e.g. /requeue ENG-42 use Auth0)", p.handleRequeue)
}

// handleStatus replies with a snapshot of in-progress tickets, worker slot
// usage, and session counters.
func (p *Pipeline) handleStatus(_ context.Context, _ string) string {
	p.mu.Lock()
	active := make([]string, 0, len(p.active))
	for id := range p.active {
		active = append(active, id)
	}
	succ := p.successCount
	fail := p.failCount
	dispatches := p.totalDispatches
	maxD := p.cfg.MaxDispatches
	maxC := p.cfg.MaxConcurrent
	p.mu.Unlock()

	sort.Strings(active)
	uptime := time.Since(p.sessionStart).Round(time.Second)

	var b strings.Builder
	b.WriteString("*Nightshift Status*\n\n")

	if len(active) == 0 {
		fmt.Fprintf(&b, "*Active runs:* 0/%d (idle)\n", maxC)
	} else {
		fmt.Fprintf(&b, "*Active runs:* %d/%d\n", len(active), maxC)
		for _, id := range active {
			fmt.Fprintf(&b, "• %s\n", notify.EscapeMarkdown(id))
		}
	}

	b.WriteString("\n*Session:*\n")
	fmt.Fprintf(&b, "✅ %d PRs created\n", succ)
	fmt.Fprintf(&b, "❌ %d failed\n", fail)
	fmt.Fprintf(&b, "📦 %d/%d dispatches used\n", dispatches, maxD)
	fmt.Fprintf(&b, "⏱ Uptime: %s\n", uptime)
	return b.String()
}

// handleKill terminates the Claude run for a specific ticket.
func (p *Pipeline) handleKill(_ context.Context, args string) string {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "Usage: /kill <ticket>\n\nExample: /kill ENG-42"
	}
	identifier := normalizeIdentifier(fields[0], p.cfg.LinearTeamKey)
	if identifier == "" {
		return "Usage: /kill <ticket>\n\nExample: /kill ENG-42"
	}

	if err := p.KillRun(identifier); err != nil {
		return fmt.Sprintf("Could not kill %s: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
	}
	return fmt.Sprintf("🔪 Killed run for %s", notify.EscapeMarkdown(identifier))
}

// handleRequeue looks up a ticket on Linear, appends the caller's context as
// a comment, and moves the ticket back to the trigger state/label so the next
// poll picks it up.
func (p *Pipeline) handleRequeue(ctx context.Context, args string) string {
	parts := strings.SplitN(args, " ", 2)
	if len(parts) == 0 || parts[0] == "" {
		return "Usage: /requeue <ticket> <extra context>\n\nExample: /requeue ENG-42 use the Auth0 provider"
	}

	identifier := normalizeIdentifier(parts[0], p.cfg.LinearTeamKey)
	if identifier == "" {
		return "Usage: /requeue <ticket> <extra context>\n\nExample: /requeue ENG-42 use the Auth0 provider"
	}

	extraContext := ""
	if len(parts) > 1 {
		extraContext = strings.TrimSpace(parts[1])
	}

	// Look up the issue on Linear.
	issue, err := p.linear.GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Sprintf("Could not find ticket %s: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
	}

	// Post the user's context as a comment.
	if extraContext != "" {
		comment := fmt.Sprintf("💬 **Requeued via Telegram**\n\n%s", extraContext)
		if err := p.linear.Comment(ctx, issue.ID, comment); err != nil {
			return fmt.Sprintf("Found %s but failed to post comment: %s",
				notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
		}
	}

	// Move to trigger state/label so the next poll picks it up.
	if p.cfg.TriggerMode == "label" && p.labelID != "" {
		if err := p.linear.AddLabel(ctx, issue.ID, p.labelID); err != nil {
			return fmt.Sprintf("Commented on %s but failed to add trigger label: %s",
				notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
		}
	} else if p.states.Trigger != "" {
		if err := p.linear.SetState(ctx, issue.ID, p.states.Trigger); err != nil {
			return fmt.Sprintf("Commented on %s but failed to move to trigger state: %s",
				notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
		}
	}

	reply := fmt.Sprintf("✅ %s requeued", notify.EscapeMarkdown(identifier))
	if extraContext != "" {
		display := extraContext
		if len(display) > 100 {
			display = display[:100] + "..."
		}
		reply += fmt.Sprintf("\nContext added: %s", notify.EscapeMarkdown(display))
	}
	return reply
}

// normalizeIdentifier converts user input to the standard "ENG-42" format.
// Accepts: "ENG-42", "eng-42", "42" (just the number — team key is prepended).
func normalizeIdentifier(input, teamKey string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	// If it's just a number, prepend the team key.
	if _, err := strconv.Atoi(input); err == nil {
		return strings.ToUpper(teamKey) + "-" + input
	}
	return strings.ToUpper(input)
}
