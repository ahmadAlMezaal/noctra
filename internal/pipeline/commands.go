package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/budget"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
	"github.com/ahmadAlMezaal/noctra/internal/telegram"
)

// registerCommands wires the Telegram command handlers that require a running
// pipeline (status, kill, requeue). Called from Run when Telegram is enabled.
func (p *Pipeline) registerCommands(d *telegram.Dispatcher) {
	d.Register("status", "Show active runs and session stats", p.handleStatus)
	d.Register("tickets", "Linear ticket counts by state for a project (e.g. /tickets Noctra)", p.handleTickets)
	d.Register("ticket", "Show a ticket's details (e.g. /ticket ENG-42)", p.handleTicket)
	d.Register("search-tickets", "Search Linear tickets by text (e.g. /search-tickets auth login)", p.handleSearch)
	d.Register("find", "Alias for /search-tickets", p.handleSearch)
	d.Register("start", "Start a ticket on the next poll (e.g. /start ENG-42)", p.handleStart)
	d.Register("move", "Move a ticket to a Linear state (e.g. /move ENG-42 \"In Review\")", p.handleMove)
	d.Register("pause", "Pause new dispatches without killing active runs", p.handlePause)
	d.Register("resume", "Resume new dispatches after /pause", p.handleResume)
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
	operatorPaused := p.paused
	p.mu.Unlock()

	sort.Strings(active)
	uptime := time.Since(p.sessionStart).Round(time.Second)

	var b strings.Builder
	b.WriteString("*Noctra Status*\n\n")
	if operatorPaused {
		b.WriteString("⏸ *Dispatch:* paused by operator\n\n")
	} else {
		b.WriteString("▶️ *Dispatch:* running\n\n")
	}

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
	fmt.Fprintf(&b, "📦 %d/%d dispatches today\n", dispatches, maxD)
	fmt.Fprintf(&b, "⏱ Uptime: %s\n", uptime)

	// Budget / usage stats (ENG-217).
	bs := p.budget.Stats()
	if bs.SessionTokens > 0 || bs.SessionCostUSD > 0 || bs.HasCaps() {
		b.WriteString("\n*Budget:*\n")
		if bs.SessionTokens > 0 || bs.SessionCostUSD > 0 {
			usageLine := fmt.Sprintf("💰 Session: %s tokens", budget.FormatTokens(bs.SessionTokens))
			if bs.SessionCostUSD > 0 {
				usageLine += fmt.Sprintf(" ($%.2f)", bs.SessionCostUSD)
			}
			fmt.Fprintf(&b, "%s\n", usageLine)
		}
		if bs.MaxDailyTokens > 0 {
			fmt.Fprintf(&b, "📊 Tokens: %s/%s today\n",
				budget.FormatTokens(bs.DailyTokens), budget.FormatTokens(bs.MaxDailyTokens))
		}
		if bs.MaxDailyUSD > 0 {
			fmt.Fprintf(&b, "💵 Cost: $%.2f/$%.2f today\n", bs.DailyCostUSD, bs.MaxDailyUSD)
		}
	}
	if bs.Paused {
		pauseMsg := fmt.Sprintf("⏸ Paused: %s", notify.EscapeMarkdown(bs.PauseReason))
		if !bs.PausedUntil.IsZero() {
			pauseMsg += fmt.Sprintf(" — resuming at %s", bs.PausedUntil.UTC().Format("15:04 UTC"))
		}
		fmt.Fprintf(&b, "\n%s\n", pauseMsg)
	}

	return b.String()
}

// handleStart moves a ticket into the configured trigger state/label so the
// next poll dispatches it.
func (p *Pipeline) handleStart(ctx context.Context, args string) string {
	identifier := normalizeIdentifier(strings.TrimSpace(args), p.cfg.LinearTeamKey)
	if identifier == "" {
		return "Usage: /start <ticket>\n\nExample: /start ENG-42"
	}
	if p.isActiveRun(identifier) {
		return fmt.Sprintf("%s is already running; use /kill if you need to stop it first.",
			notify.EscapeMarkdown(identifier))
	}

	issue, err := p.linear.GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Sprintf("Could not find ticket %s: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
	}
	if p.isActiveRun(issue.Identifier) {
		return fmt.Sprintf("%s is already running; use /kill if you need to stop it first.",
			notify.EscapeMarkdown(issue.Identifier))
	}
	if err := p.triggerIssue(ctx, issue); err != nil {
		return fmt.Sprintf("Found %s but failed to start it: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
	}
	return fmt.Sprintf("✅ %s will start on the next poll", notify.EscapeMarkdown(identifier))
}

// handleMove moves a ticket to any workflow state in its owning Linear team.
func (p *Pipeline) handleMove(ctx context.Context, args string) string {
	identifier, stateName := parseMoveArgs(args, p.cfg.LinearTeamKey)
	if identifier == "" || stateName == "" {
		return "Usage: /move <ticket> <state>\n\nExample: /move ENG-42 \"In Review\""
	}
	if p.isActiveRun(identifier) {
		return fmt.Sprintf("%s is already running; use /kill if you need to stop it first.",
			notify.EscapeMarkdown(identifier))
	}

	issue, err := p.linear.GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Sprintf("Could not find ticket %s: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
	}
	if p.isActiveRun(issue.Identifier) {
		return fmt.Sprintf("%s is already running; use /kill if you need to stop it first.",
			notify.EscapeMarkdown(issue.Identifier))
	}
	teamKey := p.cfg.LinearTeamKey
	if issue.Team != nil && issue.Team.Key != "" {
		teamKey = issue.Team.Key
	}

	stateID, available, err := p.linear.ResolveStateID(ctx, teamKey, stateName)
	if err != nil {
		if len(available) > 0 {
			return fmt.Sprintf("State %q not found for team %s.\n\nAvailable states: %s",
				notify.EscapeMarkdown(stateName),
				notify.EscapeMarkdown(teamKey),
				notify.EscapeMarkdown(strings.Join(available, ", ")))
		}
		return fmt.Sprintf("Could not resolve state for %s: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
	}
	if err := p.linear.SetState(ctx, issue.ID, stateID); err != nil {
		return fmt.Sprintf("Found %s but failed to move it: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
	}
	return fmt.Sprintf("✅ %s moved to %s",
		notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(stateName))
}

func (p *Pipeline) handlePause(_ context.Context, _ string) string {
	alreadyPaused := p.PauseDispatch()
	if alreadyPaused {
		return "⏸ Dispatch is already paused. Active runs will continue."
	}
	return "⏸ Dispatch paused. Active runs will continue."
}

func (p *Pipeline) handleResume(_ context.Context, _ string) string {
	wasPaused := p.ResumeDispatch()
	if !wasPaused {
		return "▶️ Dispatch is already running."
	}
	return "▶️ Dispatch resumed."
}

// handleTickets reports a project's Linear ticket counts grouped by workflow
// state. Directive-only routing means there's no registry to enumerate, so a
// project name is required.
//
//	/tickets <project>   counts per state for that project
func (p *Pipeline) handleTickets(ctx context.Context, args string) string {
	project := strings.TrimSpace(args)
	if project == "" {
		return "Usage: /tickets <project>\n\nExample: /tickets Noctra"
	}

	var b strings.Builder
	b.WriteString("*Linear tickets*\n\n")
	p.writeProjectCounts(ctx, &b, project)
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
		return "Usage: /search-tickets <terms>\n\nExample: /search-tickets auth login"
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

	if err := p.triggerIssue(ctx, issue); err != nil {
		return fmt.Sprintf("Commented on %s but failed to requeue it: %s",
			notify.EscapeMarkdown(identifier), notify.EscapeMarkdown(err.Error()))
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

func (p *Pipeline) triggerIssue(ctx context.Context, issue linear.Issue) error {
	if p.cfg.TriggerMode == "label" && p.labelID != "" {
		return p.linear.AddLabel(ctx, issue.ID, p.labelID)
	}
	if p.states.Trigger != "" {
		return p.linear.SetState(ctx, issue.ID, p.states.Trigger)
	}
	return fmt.Errorf("trigger is not resolved")
}

func parseMoveArgs(args, teamKey string) (string, string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", ""
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", ""
	}
	identifier := normalizeIdentifier(fields[0], teamKey)
	stateName := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
	if len(stateName) >= 2 {
		first := stateName[0]
		last := stateName[len(stateName)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			stateName = strings.TrimSpace(stateName[1 : len(stateName)-1])
		}
	}
	return identifier, stateName
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
