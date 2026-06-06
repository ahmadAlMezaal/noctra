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
	d.Register("tickets", "Linear ticket counts by state, or list a state (e.g. /tickets Nightshift Next)", p.handleTickets)
	d.Register("ticket", "Show a ticket's details (e.g. /ticket ENG-42)", p.handleTicket)
	d.Register("search", "Search Linear tickets by text (e.g. /search auth login)", p.handleSearch)
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

// handleTickets reports Linear ticket counts grouped by workflow state, or —
// when a trailing state is given — lists the tickets in that state.
//
//	/tickets                     counts for every project in repos.json
//	/tickets <project>           counts per state for one project
//	/tickets <project> <state>   list the tickets in that state
func (p *Pipeline) handleTickets(ctx context.Context, args string) string {
	args = strings.TrimSpace(args)

	if args == "" {
		names := p.cfg.Registry.ProjectNames()
		if len(names) == 0 {
			return "No projects registered in repos.json.\n\nUsage: /tickets <project> [state]"
		}
		var b strings.Builder
		b.WriteString("*Linear tickets by project*\n\n")
		for i, name := range names {
			if i > 0 {
				b.WriteString("\n")
			}
			p.writeProjectCounts(ctx, &b, name)
		}
		return b.String()
	}

	project, state := p.splitProjectState(args)
	if state != "" {
		return p.listTickets(ctx, project, state)
	}

	var b strings.Builder
	b.WriteString("*Linear tickets*\n\n")
	p.writeProjectCounts(ctx, &b, project)
	return b.String()
}

// splitProjectState separates a "/tickets" argument into a project name and an
// optional trailing state. It matches the longest registered project name that
// is a case-insensitive prefix of args; the remainder (if any) is the state.
// With no registry match the whole string is treated as the project name (so
// counts still work for projects not in repos.json).
func (p *Pipeline) splitProjectState(args string) (project, state string) {
	lower := strings.ToLower(args)
	best := ""
	for _, name := range p.cfg.Registry.ProjectNames() {
		ln := strings.ToLower(name)
		if (lower == ln || strings.HasPrefix(lower, ln+" ")) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return args, ""
	}
	return best, strings.TrimSpace(args[len(best):])
}

// listTickets lists the tickets in one project filtered to a single state.
func (p *Pipeline) listTickets(ctx context.Context, project, state string) string {
	issues, err := p.linear.ListProjectIssues(ctx, project, state, 25)
	if err != nil {
		return fmt.Sprintf("Could not list %s tickets: %s",
			notify.EscapeMarkdown(project), notify.EscapeMarkdown(err.Error()))
	}
	if len(issues) == 0 {
		return fmt.Sprintf("No *%s* tickets in *%s* (check the state name).",
			notify.EscapeMarkdown(state), notify.EscapeMarkdown(project))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "*%s* — %s (%d)\n",
		notify.EscapeMarkdown(project), notify.EscapeMarkdown(state), len(issues))
	for _, is := range issues {
		fmt.Fprintf(&b, "• %s %s\n",
			notify.EscapeMarkdown(is.Identifier), notify.EscapeMarkdown(is.Title))
	}
	return b.String()
}

// handleTicket shows a single ticket's details, looked up by identifier.
func (p *Pipeline) handleTicket(ctx context.Context, args string) string {
	id := normalizeIdentifier(strings.TrimSpace(args), p.cfg.LinearTeamKey)
	if id == "" {
		return "Usage: /ticket <id>\n\nExample: /ticket ENG-42"
	}

	issue, err := p.linear.GetIssueByIdentifier(ctx, id)
	if err != nil {
		return fmt.Sprintf("Could not find ticket %s: %s",
			notify.EscapeMarkdown(id), notify.EscapeMarkdown(err.Error()))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "*%s* — %s\n",
		notify.EscapeMarkdown(issue.Identifier), notify.EscapeMarkdown(issue.Title))
	if s := issue.StateName(); s != "" {
		fmt.Fprintf(&b, "State: %s\n", notify.EscapeMarkdown(s))
	}
	if pr := issue.ProjectName(); pr != "" {
		fmt.Fprintf(&b, "Project: %s\n", notify.EscapeMarkdown(pr))
	}
	if a := issue.AssigneeName(); a != "" {
		fmt.Fprintf(&b, "Assignee: %s\n", notify.EscapeMarkdown(a))
	}
	if issue.URL != "" {
		fmt.Fprintf(&b, "%s\n", notify.EscapeMarkdown(issue.URL))
	}
	if d := snippet(issue.Description, 280); d != "" {
		fmt.Fprintf(&b, "\n%s\n", notify.EscapeMarkdown(d))
	}
	return b.String()
}

// handleSearch lists tickets whose title/description match the given text.
func (p *Pipeline) handleSearch(ctx context.Context, args string) string {
	term := strings.TrimSpace(args)
	if term == "" {
		return "Usage: /search <terms>\n\nExample: /search auth login"
	}

	issues, err := p.linear.SearchIssues(ctx, term, 15)
	if err != nil {
		return fmt.Sprintf("Search failed: %s", notify.EscapeMarkdown(err.Error()))
	}
	if len(issues) == 0 {
		return fmt.Sprintf("No tickets match *%s*.", notify.EscapeMarkdown(term))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "*Search:* %s (%d)\n", notify.EscapeMarkdown(term), len(issues))
	for _, is := range issues {
		if s := is.StateName(); s != "" {
			fmt.Fprintf(&b, "• %s [%s] %s\n",
				notify.EscapeMarkdown(is.Identifier), notify.EscapeMarkdown(s),
				notify.EscapeMarkdown(is.Title))
		} else {
			fmt.Fprintf(&b, "• %s %s\n",
				notify.EscapeMarkdown(is.Identifier), notify.EscapeMarkdown(is.Title))
		}
	}
	return b.String()
}

// snippet trims s and truncates it to at most maxRunes runes, appending an
// ellipsis when it had to cut.
func snippet(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return strings.TrimSpace(string(r[:maxRunes])) + "…"
}

// writeProjectCounts renders one project's per-state ticket counts into b.
func (p *Pipeline) writeProjectCounts(ctx context.Context, b *strings.Builder, project string) {
	counts, err := p.linear.ProjectIssueCounts(ctx, project)
	if err != nil {
		fmt.Fprintf(b, "*%s* — error: %s\n",
			notify.EscapeMarkdown(project), notify.EscapeMarkdown(err.Error()))
		return
	}
	if len(counts) == 0 {
		fmt.Fprintf(b, "*%s* — no tickets found\n", notify.EscapeMarkdown(project))
		return
	}

	total := 0
	for _, c := range counts {
		total += c.Count
	}
	fmt.Fprintf(b, "*%s* — %d total\n", notify.EscapeMarkdown(project), total)
	for _, c := range counts {
		fmt.Fprintf(b, "• %s: %d\n", notify.EscapeMarkdown(c.State), c.Count)
	}
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
