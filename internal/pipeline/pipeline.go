// Package pipeline runs Noctra's main loop: poll Linear, dispatch a
// bounded worker pool of process_ticket goroutines, and shut down cleanly on
// signal or rate-limit. The daily dispatch cap pauses dispatching rather than
// stopping the process.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
	"github.com/ahmadAlMezaal/noctra/internal/budget"
	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/dashboard"
	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/selfupdate"
	"github.com/ahmadAlMezaal/noctra/internal/source"
	"github.com/ahmadAlMezaal/noctra/internal/state"
	"github.com/ahmadAlMezaal/noctra/internal/sweep"
	"github.com/ahmadAlMezaal/noctra/internal/telegram"
	"github.com/ahmadAlMezaal/noctra/internal/watch"
)

// Version is the running build version, set by main before Run so the startup
// update check can compare it against the latest published release. Empty/"dev"
// for local builds, which suppresses the check entirely.
var Version = ""

// Pipeline holds the wiring shared by every ticket the agent processes.
type Pipeline struct {
	cfg      *config.Config
	linear   *linear.Client
	sources  []source.TicketSource
	resolver *repo.Resolver
	notifier *notify.Multi
	review   *review.Gate
	agent    agent.Backend // selected coding-agent CLI (claude / codex / copilot / antigravity)
	states   linear.StateIDs

	// Label-mode trigger — resolved at startup when cfg.TriggerMode == "label".
	labelID string

	// Auto-iterate plumbing — all nil when cfg.AutoIteratePRs is false.
	store   *state.Store
	gh      *github.Client
	watcher *watch.Watcher

	// Budget tracker — always non-nil (no-op when no caps configured).
	budget *budget.Tracker

	// Sweep scheduler — nil when cfg.SweepEnabled is false.
	sweeper *sweep.Scheduler

	// Plan-confirm label ID — resolved at startup when plan-confirm is enabled
	// and the label name resolves successfully. Empty if resolution fails
	// (per-ticket label detection degrades to global-only mode).
	planConfirmLabelID string

	// Dashboard server — nil when cfg.DashboardAddr is empty.
	dash *dashboard.Server
	hub  *dashboard.Hub

	sessionStart time.Time

	mu                sync.Mutex
	active            map[string]struct{}           // identifiers in-flight
	activeRepos       map[string]string             // identifier → repo slug (for dashboard grouping)
	cancels           map[string]context.CancelFunc // per-ticket cancel (for /kill)
	killed            map[string]struct{}           // tickets killed via /kill
	failedAttempts    map[string]int                // per-ticket retry counter
	approvedPlans     map[string]string             // plan-confirm: approved plans keyed by identifier (ENG-221)
	skipped           map[string]struct{}           // non-transient failures — never re-dispatched
	totalDispatches   int                           // per-UTC-day dispatch count (MAX_DISPATCHES)
	dispatchWindow    time.Time
	dispatchCapped    bool
	successCount      int
	failCount         int
	rateLimitDetected bool // only used when RateLimitStrategy=shutdown
	paused            bool // operator pause via Telegram; active runs continue
}

// New constructs a Pipeline. It does not perform any I/O — call Run to start.
func New(cfg *config.Config) *Pipeline {
	// config.Validate already guarantees a known backend; fall back to Claude
	// defensively if a caller skipped validation.
	backend, err := agent.New(cfg.AgentBackend)
	if err != nil {
		slog.Warn("unknown agent backend; falling back to claude",
			"backend", cfg.AgentBackend, "err", err)
		backend, _ = agent.New(config.DefaultAgentBackend)
	}

	// Open the state store up front if any feature needs it: auto-iterate,
	// sweep, plan-confirm, actor=app OAuth refresh persistence, or dashboard.
	var store *state.Store
	if cfg.AutoIteratePRs || cfg.SweepEnabled || cfg.PlanConfirm || cfg.PlanConfirmLabel != "" || cfg.ActorAppConfigured() || cfg.DashboardAddr != "" {
		s, err := state.OpenMigrating(cfg.StateDB, cfg.StateFile)
		if err != nil {
			slog.Warn("state store open failed", "path", cfg.StateDB, "legacy_path", cfg.StateFile, "err", err)
		} else {
			store = s
		}
	}

	linearClient := newLinearClient(cfg, store)
	p := &Pipeline{
		cfg:      cfg,
		linear:   linearClient,
		resolver: repo.FromConfig(cfg),
		notifier: buildNotifier(cfg),
		review:   review.NewWithMode(cfg.GeminiMode, cfg.GeminiAPIKey, cfg.GeminiModel),
		agent:    backend,
		budget: budget.New(budget.Config{
			MaxDailyTokens: cfg.MaxDailyTokens,
			MaxDailyUSD:    cfg.MaxDailyUSD,
		}),
		active:         map[string]struct{}{},
		activeRepos:    map[string]string{},
		cancels:        map[string]context.CancelFunc{},
		killed:         map[string]struct{}{},
		failedAttempts: map[string]int{},
		skipped:        map[string]struct{}{},
		sessionStart:   time.Now(),
		store:          store,
	}
	p.sources = buildTicketSources(cfg, linearClient)

	// Alert when the Linear app identity degrades to the personal API key.
	p.linear.OnDegrade = func(cause error) {
		p.notifier.Send(context.Background(),
			"⚠️ Linear app identity (actor=app) failed to authenticate — now posting as your personal user. Re-mint the OAuth credentials.")
	}

	// Auto-iterate is opt-in. When disabled, gh/watcher stay nil and
	// the run loop never starts the PR-poller goroutine.
	if cfg.AutoIteratePRs && store != nil {
		p.gh = github.New()

		// Directive-only routing has no static repo registry: the watcher
		// discovers which repos to poll from the on-demand clones on disk,
		// re-read on every scan (the set grows as tickets are dispatched).
		p.watcher = watch.New(p.gh, store, p.resolver.AllRepoRemotes, cfg.TrustedReviewers)
	}

	// Sweep is opt-in. When disabled, sweeper stays nil and the run loop
	// never starts the sweep-scheduler goroutine.
	if cfg.SweepEnabled && store != nil {
		tasks := sweep.FilterTasks(cfg.SweepTasks)
		if len(tasks) == 0 {
			slog.Warn("sweep: no tasks enabled; feature disabled")
		} else {
			var schedule *sweep.CronSchedule
			if cfg.SweepSchedule != "" {
				parsed, err := sweep.ParseCron(cfg.SweepSchedule)
				switch {
				case err != nil:
					slog.Warn("sweep: invalid SWEEP_SCHEDULE; using interval instead",
						"schedule", cfg.SweepSchedule, "err", err)
				case parsed.Next(time.Now()).IsZero():
					slog.Warn("sweep: SWEEP_SCHEDULE never matches a real date; using interval instead",
						"schedule", cfg.SweepSchedule)
				default:
					schedule = parsed
				}
			}
			p.sweeper = sweep.NewScheduler(store, p.resolver, tasks, cfg.SweepInterval, cfg.SweepMaxTasks, schedule, cfg.SweepRepos)
		}
	}

	// Dashboard — constructed eagerly so banner() can inspect it, but
	// ListenAndServe is deferred to Run (which validates the token).
	if cfg.DashboardAddr != "" {
		addr := normalizeDashboardAddr(cfg.DashboardAddr)
		p.hub = dashboard.NewHub(64)
		prov := dashboard.Providers{
			Store:           store,
			MaxPRIterations: cfg.MaxPRIterations,
			RepoPaths:       p.resolver.AllRepoPaths,
			LogDir:          cfg.LogDir,
			Hub:             p.hub,
		}
		if p.sweeper != nil {
			prov.SweepTasks = sweep.FilterTasks(cfg.SweepTasks)
		}
		p.dash = dashboard.New(addr, cfg.DashboardToken, func() any {
			return p.Snapshot()
		}, prov)
	}

	return p
}

// normalizeDashboardAddr binds to localhost when the user supplies only a port
// (e.g. ":8080") so the dashboard is not accidentally exposed on all interfaces.
func normalizeDashboardAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// Run blocks until ctx is canceled or a rate-limit is detected
// (RATE_LIMIT_STRATEGY=shutdown). The daily dispatch cap only pauses
// dispatching. It always waits for in-flight workers to finish before
// returning, and prints a session summary on the way out.
func (p *Pipeline) Run(ctx context.Context) error {
	for _, d := range []string{p.cfg.LogDir, p.cfg.WorktreeBase, p.cfg.ReposBase} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	for _, src := range p.sources {
		sourceCtx, sourceCancel := context.WithTimeout(ctx, 30*time.Second)
		err := src.Prepare(sourceCtx)
		sourceCancel()
		if err != nil {
			return fmt.Errorf("prepare %s source: %w", src.Name(), err)
		}
		if ls, ok := src.(*source.LinearSource); ok {
			p.states = ls.StateIDs()
			p.labelID = ls.TriggerLabelID()
			p.planConfirmLabelID = ls.PlanConfirmLabelID()
			slog.Info("resolved Linear source", "trigger_mode", p.cfg.TriggerMode, "in_review", p.cfg.InReviewState)
			if p.cfg.TriggerMode == "label" {
				slog.Info("resolved Linear label", "label", p.cfg.TriggerLabel, "id", p.labelID)
			}
			if p.cfg.PlanConfirmLabel != "" && p.planConfirmLabelID == "" {
				slog.Warn("plan-confirm label not found — per-ticket label activation disabled",
					"label", p.cfg.PlanConfirmLabel)
			}
		}
	}

	// Fail fast: dashboard address set but no token.
	if p.cfg.DashboardAddr != "" && p.cfg.DashboardToken == "" {
		return fmt.Errorf("DASHBOARD_ADDR is set but DASHBOARD_TOKEN is empty — refusing to start an unauthenticated dashboard")
	}

	p.startupCleanup(ctx)
	p.banner()
	if p.cfg.TriggerMode == "label" {
		p.notifier.Send(ctx, fmt.Sprintf("🌙 *Noctra started*\nWatching label \"%s\" for %s tickets",
			notify.EscapeMarkdown(p.cfg.TriggerLabel), notify.EscapeMarkdown(p.cfg.LinearTeamKey)))
	} else {
		p.notifier.Send(ctx, fmt.Sprintf("🌙 *Noctra started*\nWatching \"%s\" for %s tickets",
			notify.EscapeMarkdown(p.cfg.TriggerState), notify.EscapeMarkdown(p.cfg.LinearTeamKey)))
	}

	// Best-effort update check: never blocks startup, swallows all errors, and
	// does nothing for dev builds (IsNewer returns false). Logs a line and
	// pings Telegram once if a newer release is published.
	go p.checkForUpdate(ctx)

	var wg sync.WaitGroup

	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()

	// Start the Telegram command listener alongside the poll loop if configured.
	// The listener shares the WaitGroup so shutdown drains it like any other goroutine.
	if p.cfg.TelegramEnabled && p.cfg.TelegramBotToken != "" && p.cfg.TelegramChatID != "" {
		listener := telegram.New(p.cfg.TelegramBotToken, p.cfg.TelegramChatID)
		p.registerCommands(listener.Dispatcher())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := listener.Run(loopCtx); err != nil {
				slog.Warn("telegram listener stopped", "err", err)
			}
		}()
		slog.Info("telegram command listener started")
	}

	// Start the dashboard server if configured. Shares the WaitGroup so
	// shutdown drains it alongside the poll loop and other goroutines.
	if p.dash != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.dash.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Warn("dashboard server stopped", "err", err)
			}
		}()
		// Shut the server down when the loop context is cancelled.
		go func() {
			<-loopCtx.Done()
			_ = p.dash.Shutdown(context.Background())
		}()
	}

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	// One poll right away so we don't sit idle for the first interval.
	p.pollOnce(ctx, &wg)

	// PR watcher runs on its own ticker if auto-iterate is enabled. Lives
	// inside this Run so it shares the WaitGroup — graceful shutdown waits
	// for in-flight iterations the same way it waits for fresh dispatches.
	if p.watcher != nil {
		wg.Add(1)
		go p.runWatcher(loopCtx, &wg)
	}

	// Sweep scheduler runs its own loop if enabled. Shares the WaitGroup
	// so shutdown drains in-flight sweep tasks.
	if p.sweeper != nil {
		wg.Add(1)
		go p.runSweepLoop(loopCtx, &wg)
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("🌅 shutting down — waiting for active tasks")
			drainAndStop(stopLoop, &wg)
			p.summary(ctx)
			return nil

		case <-ticker.C:
			p.mu.Lock()
			rlDetected := p.rateLimitDetected
			p.mu.Unlock()

			if rlDetected {
				slog.Info("🛑 rate limit detected — shutting down")
				drainAndStop(stopLoop, &wg)
				p.summary(ctx)
				return nil
			}

			// Budget pause: skip dispatching but keep the loop alive.
			if paused, until, reason := p.budget.IsPaused(); paused {
				slog.Debug("⏸ dispatching paused",
					"reason", reason, "until", until.Format(time.RFC3339))
				continue
			}

			// Check budget caps on every tick so concurrent in-flight runs
			// that pushed usage over the cap are caught promptly (rather
			// than waiting for the next completed run to call flagBudgetExceeded).
			if reason := p.budget.ExceededReason(); reason != "" {
				p.flagBudgetExceeded(reason)
				p.notifier.Send(ctx, fmt.Sprintf(
					"⏸ *Daily budget exceeded*\n%s\nDispatching paused until next UTC midnight.",
					notify.EscapeMarkdown(reason)))
				continue
			}

			// ctx, not loopCtx: a self-shutdown lets in-flight dispatches drain;
			// only the long-lived loops are cancelled.
			p.pollOnce(ctx, &wg)
		}
	}
}

// drainAndStop cancels before waiting; reversing the order deadlocks.
func drainAndStop(stop context.CancelFunc, wg *sync.WaitGroup) {
	stop()
	wg.Wait()
}

// dispatchCapReached reports whether today's dispatch count has hit the cap.
// A max of 0 (or negative) means unlimited.
func dispatchCapReached(max, count int) bool {
	return max > 0 && count >= max
}

// rollDispatchWindow resets the daily dispatch counter at UTC midnight.
// Caller must hold p.mu.
func (p *Pipeline) rollDispatchWindow(now time.Time) {
	day := now.UTC().Truncate(24 * time.Hour)
	if !day.Equal(p.dispatchWindow) {
		p.dispatchWindow = day
		p.totalDispatches = 0
		p.dispatchCapped = false
	}
}

func (p *Pipeline) pollOnce(ctx context.Context, wg *sync.WaitGroup) {
	p.mu.Lock()
	if p.paused {
		p.mu.Unlock()
		slog.Debug("⏸ dispatching paused by operator")
		return
	}
	p.rollDispatchWindow(time.Now())
	inFlight := len(p.active)
	available := p.cfg.MaxConcurrent - inFlight
	capped := dispatchCapReached(p.cfg.MaxDispatches, p.totalDispatches)
	notifyCap := capped && !p.dispatchCapped
	if notifyCap {
		p.dispatchCapped = true
	}
	p.mu.Unlock()

	// Daily cap reached: pause dispatching (resumes after the UTC-midnight reset); alert once.
	if capped {
		if notifyCap {
			slog.Info("⏸ daily dispatch cap reached — pausing until UTC midnight",
				"limit", p.cfg.MaxDispatches)
			p.notifier.Send(ctx, fmt.Sprintf(
				"⏸ *Daily dispatch cap reached* (%d)\nPausing new dispatches until next UTC midnight.",
				p.cfg.MaxDispatches))
		}
		return
	}

	// Check for approved plans before fetching new tickets so approved
	// plans can be dispatched in the same cycle (ENG-221).
	p.pollPlanApprovals(ctx, wg, &available)

	if available <= 0 {
		slog.Debug("at capacity", "active", inFlight, "max", p.cfg.MaxConcurrent)
		return
	}

	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	issues, err := p.fetchTickets(fctx)
	cancel()
	if err != nil {
		slog.Warn("fetch tickets failed", "err", err)
		return
	}

	triggerDisplay := p.cfg.TriggerState
	if p.cfg.TriggerMode == "label" {
		triggerDisplay = "label:" + p.cfg.TriggerLabel
	}
	slog.Info("poll",
		"trigger", triggerDisplay,
		"found", len(issues),
		"active", inFlight,
		"max", p.cfg.MaxConcurrent,
	)

	for _, issue := range issues {
		if available <= 0 {
			break
		}

		p.mu.Lock()
		if dispatchCapReached(p.cfg.MaxDispatches, p.totalDispatches) {
			p.mu.Unlock()
			return
		}
		if _, dupe := p.active[issue.Identifier]; dupe {
			p.mu.Unlock()
			slog.Info("skipping (already in progress)", "id", issue.Identifier)
			continue
		}
		if p.failedAttempts[issue.Identifier] >= p.cfg.MaxRetries {
			p.mu.Unlock()
			slog.Info("skipping (max retries hit)", "id", issue.Identifier,
				"attempts", p.failedAttempts[issue.Identifier])
			continue
		}
		if _, skip := p.skipped[issue.Identifier]; skip {
			p.mu.Unlock()
			slog.Debug("skipping (non-transient failure)", "id", issue.Identifier)
			continue
		}
		// Skip tickets that have a pending plan awaiting approval (ENG-221).
		// They stay in the trigger state but should not be re-dispatched until
		// the human approves or the plan is cleared.
		if p.hasPendingPlan(issue.Identifier) {
			p.mu.Unlock()
			slog.Debug("skipping (plan awaiting approval)", "id", issue.Identifier)
			continue
		}
		ticketCtx, ticketCancel := context.WithCancel(ctx)
		p.active[issue.Identifier] = struct{}{}
		p.cancels[issue.Identifier] = ticketCancel
		p.totalDispatches++
		p.mu.Unlock()
		p.publishDashboardChange()

		available--

		slog.Info("🎯 dispatching", "id", issue.Identifier, "title", issue.Title)

		wg.Add(1)
		go func(iss source.Ticket) {
			defer wg.Done()
			defer p.markDone(iss.Identifier)
			p.process(ticketCtx, iss)
		}(issue)
	}
}

func (p *Pipeline) fetchTickets(ctx context.Context) ([]source.Ticket, error) {
	var out []source.Ticket
	failures := 0
	for _, src := range p.sources {
		tickets, err := src.Fetch(ctx)
		if err != nil {
			failures++
			slog.Warn("fetch source failed", "source", src.Name(), "err", err)
			continue
		}
		out = append(out, tickets...)
	}
	if failures > 0 && failures == len(p.sources) {
		return nil, fmt.Errorf("all ticket sources failed")
	}
	return out, nil
}

func (p *Pipeline) markDone(id string) {
	p.mu.Lock()
	delete(p.active, id)
	if cancel, ok := p.cancels[id]; ok {
		cancel()
		delete(p.cancels, id)
	}
	delete(p.killed, id)
	p.mu.Unlock()
	p.publishDashboardChange()
}

// isKilled reports whether a ticket was terminated via /kill. Used to skip
// normal error handling (bump-failed, linearBackToTrigger) when the user
// intentionally stopped a run.
func (p *Pipeline) isKilled(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.killed[id]
	return ok
}

func (p *Pipeline) isActiveRun(identifier string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.active[identifier]
	return ok
}

// KillRun cancels the context for an in-flight ticket, terminating any
// running Claude process. The goroutine handles worktree cleanup on return.
func (p *Pipeline) KillRun(identifier string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cancel, ok := p.cancels[identifier]
	if !ok {
		if _, active := p.active[identifier]; active {
			return fmt.Errorf("%s is active but has no cancel handle", identifier)
		}
		return fmt.Errorf("no active run for %s", identifier)
	}
	p.killed[identifier] = struct{}{}
	cancel()
	return nil
}

// PauseDispatch stops future poll dispatches while letting active runs drain.
// It returns true if dispatching was already paused.
func (p *Pipeline) PauseDispatch() bool {
	p.mu.Lock()
	alreadyPaused := p.paused
	p.paused = true
	p.mu.Unlock()
	if !alreadyPaused {
		p.publishDashboardChange()
	}
	return alreadyPaused
}

// ResumeDispatch re-enables future poll dispatches. It returns true if
// dispatching had been paused.
func (p *Pipeline) ResumeDispatch() bool {
	p.mu.Lock()
	wasPaused := p.paused
	p.paused = false
	p.mu.Unlock()
	if wasPaused {
		p.publishDashboardChange()
	}
	return wasPaused
}

func (p *Pipeline) dispatchPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

func (p *Pipeline) bumpFailed(id string) int {
	p.mu.Lock()
	p.failedAttempts[id]++
	p.failCount++
	attempts := p.failedAttempts[id]
	p.mu.Unlock()
	p.publishDashboardChange()
	return attempts
}

func (p *Pipeline) bumpSuccess() {
	p.mu.Lock()
	p.successCount++
	p.mu.Unlock()
	p.publishDashboardChange()
}

// skipPermanently marks a ticket as permanently skipped (non-transient failure
// like a project with no `Repo:` directive and no REPO_PATH fallback). The
// ticket won't be re-dispatched on future polls, and the dispatch it consumed
// is refunded so deterministic config errors don't burn the dispatch budget or
// shut the agent down.
// Idempotent: calling multiple times for the same ID only refunds once.
func (p *Pipeline) skipPermanently(id string) {
	p.mu.Lock()
	changed := false
	if _, ok := p.skipped[id]; !ok {
		p.skipped[id] = struct{}{}
		changed = true
		if p.totalDispatches > 0 {
			p.totalDispatches-- // refund — config errors shouldn't count
		}
	}
	p.mu.Unlock()
	if changed {
		p.publishDashboardChange()
	}
}

func (p *Pipeline) publishDashboardChange() {
	if p.hub != nil {
		p.hub.Publish()
	}
}

// flagRateLimit handles a detected rate limit according to the configured
// strategy. "shutdown" (legacy) sets a flag that causes the main loop to
// exit on the next tick. "pause" (default, ENG-217) pauses dispatching for
// the configured cooldown period without exiting.
func (p *Pipeline) flagRateLimit() {
	if p.cfg.RateLimitStrategy == "shutdown" {
		p.mu.Lock()
		p.rateLimitDetected = true
		p.mu.Unlock()
		p.publishDashboardChange()
		return
	}
	// Pause strategy: pause dispatching for the cooldown period.
	resumeAt := time.Now().Add(p.cfg.RateLimitCooldown)
	p.budget.Pause("rate limit", resumeAt)
	slog.Info("⏸ rate limit detected — pausing dispatches",
		"cooldown", p.cfg.RateLimitCooldown, "resume_at", resumeAt.Format(time.RFC3339))
	p.publishDashboardChange()
}

// flagBudgetExceeded pauses dispatching until the next UTC midnight when a
// daily budget cap is hit.
func (p *Pipeline) flagBudgetExceeded(reason string) {
	resumeAt := budget.NextUTCMidnight()
	p.budget.Pause(reason, resumeAt)
	slog.Info("⏸ daily budget exceeded — pausing dispatches",
		"reason", reason, "resume_at", resumeAt.Format(time.RFC3339))
	p.publishDashboardChange()
}

// rateLimited reports whether a run should be treated as having hit a usage /
// rate limit. A genuine limit makes the agent CLI FAIL, so this is only true
// for a failed run whose output carries the backend's rate-limit markers. A
// successful run is never rate-limited — even if its transcript mentions the
// words (e.g. an agent editing a file that documents rate-limit handling).
// Without the runErr gate, such a run had its completed work discarded (ENG-178).
func rateLimited(b agent.Backend, runErr error, output string) bool {
	return runErr != nil && b.HasRateLimit(output)
}

// buildNotifier constructs the multi-platform notifier from config. Every
// enabled platform gets its own backend; the Multi fans out to all of them.
func buildNotifier(cfg *config.Config) *notify.Multi {
	var backends []notify.Notifier
	var labels []string

	if tg := notify.New(cfg.TelegramEnabled, cfg.TelegramBotToken, cfg.TelegramChatID); tg.Enabled {
		backends = append(backends, tg)
		labels = append(labels, "Telegram")
	}
	if sl := notify.NewSlack(cfg.SlackWebhookURL); sl.Enabled {
		backends = append(backends, sl)
		labels = append(labels, "Slack")
	}
	if dc := notify.NewDiscord(cfg.DiscordWebhookURL); dc.Enabled {
		backends = append(backends, dc)
		labels = append(labels, "Discord")
	}

	return notify.NewMulti(backends, labels)
}

func newLinearClient(cfg *config.Config, store *state.Store) *linear.Client {
	if cfg.OAuthPartiallyConfigured() {
		slog.Warn("linear actor=app config incomplete (need both client id and secret); using personal API key")
	}
	if cfg.ActorAppConfigured() {
		var ts linear.TokenStore
		if store != nil {
			ts = store
		}
		tm := linear.NewTokenManager(linear.TokenManagerConfig{
			ClientID:     cfg.LinearOAuthClientID,
			ClientSecret: cfg.LinearOAuthClientSecret,
			RefreshToken: cfg.LinearOAuthRefreshToken,
			Scope:        cfg.LinearOAuthScope,
			Store:        ts,
		})
		c := linear.New(cfg.LinearAPIKey)
		c.TokenFn = tm.Token
		c.OnAuthError = tm.ForceRefresh
		c.FallbackAPIKey = cfg.LinearAPIKey
		return c
	}
	if cfg.LinearOAuthToken != "" {
		c := linear.NewOAuth(cfg.LinearOAuthToken)
		c.FallbackAPIKey = cfg.LinearAPIKey
		return c
	}
	return linear.New(cfg.LinearAPIKey)
}

func buildTicketSources(cfg *config.Config, linearClient *linear.Client) []source.TicketSource {
	var sources []source.TicketSource
	names := cfg.TicketSources
	if len(names) == 0 {
		names = []string{"linear"}
	}
	for _, name := range names {
		switch name {
		case "linear":
			sources = append(sources, source.NewLinear(linearClient, source.LinearConfig{
				TeamKey:          cfg.LinearTeamKey,
				TriggerMode:      cfg.TriggerMode,
				TriggerState:     cfg.TriggerState,
				TriggerLabel:     cfg.TriggerLabel,
				InReviewState:    cfg.InReviewState,
				DoneState:        cfg.DoneState,
				PlanConfirmLabel: cfg.PlanConfirmLabel,
			}))
		case "github":
			sources = append(sources, source.NewGitHubIssues(source.GitHubIssuesConfig{
				Repos:        cfg.GitHubIssuesRepos,
				TriggerLabel: cfg.GitHubTriggerLabel,
			}))
		case "jira":
			sources = append(sources, source.NewJira(source.JiraConfig{
				BaseURL:        cfg.JiraBaseURL,
				UserEmail:      cfg.JiraUserEmail,
				APIToken:       cfg.JiraAPIToken,
				Project:        cfg.JiraProject,
				TriggerStatus:  cfg.JiraTriggerStatus,
				TriggerLabel:   cfg.JiraTriggerLabel,
				InReviewStatus: cfg.JiraInReviewStatus,
			}))
		}
	}
	return sources
}

func (p *Pipeline) banner() {
	reviewMode := "Disabled"
	if p.review.Enabled() {
		reviewMode = fmt.Sprintf("Gemini %s (%s)", p.review.Mode, p.review.Model)
	}
	notifyMode := p.notifier.String()
	agentMode := fmt.Sprintf("per-ticket via label (default: %s)", p.agent.Label())
	if p.cfg.UseAgentTeams {
		agentMode += " + agent teams"
	}
	autoIterMode := "Disabled"
	if p.watcher != nil {
		autoIterMode = fmt.Sprintf("On (cap %d, poll %s)",
			p.cfg.MaxPRIterations, p.cfg.PRPollInterval)
	}
	autoReleaseMode := "Disabled"
	if p.cfg.AutoReleaseLabel {
		autoReleaseMode = fmt.Sprintf("On (default: %s)", p.cfg.DefaultReleaseBump)
	}
	sweepMode := "Disabled"
	if p.sweeper != nil {
		cadence := fmt.Sprintf("interval %s", p.cfg.SweepInterval)
		if p.cfg.SweepSchedule != "" {
			cadence = fmt.Sprintf("cron %q", p.cfg.SweepSchedule)
		}
		scope := "all cloned repos"
		if n := len(p.cfg.SweepRepos); n > 0 {
			scope = fmt.Sprintf("%d listed repo(s)", n)
		}
		sweepMode = fmt.Sprintf("On (%s, max %d tasks, %s)", cadence, p.cfg.SweepMaxTasks, scope)
	}

	// Repos are routed per-ticket from each Linear project's "Repo:" directive
	// and cloned on demand, so there's no static list at startup — report the
	// routing mode plus however many clones already exist on disk.
	repoSummary := "source Repo: directives"
	if n := len(p.resolver.AllRepoPaths()); n > 0 {
		repoSummary += fmt.Sprintf(" (%d cloned)", n)
	}
	if p.cfg.RepoPath != "" {
		repoSummary += " + REPO_PATH fallback"
	}

	fmt.Println()
	fmt.Println("🌙 Noctra (Go)")
	fmt.Printf("   Repos:          %s\n", repoSummary)
	fmt.Printf("   Sources:        %s\n", strings.Join(p.cfg.TicketSources, ", "))
	fmt.Printf("   Worktrees:      %s\n", p.cfg.WorktreeBase)
	fmt.Printf("   Team:           %s\n", p.cfg.LinearTeamKey)
	linearIdentity := "personal API key"
	switch {
	case p.cfg.ActorAppConfigured():
		linearIdentity = "Noctra app (OAuth actor=app, auto-renew)"
	case p.cfg.LinearOAuthToken != "":
		linearIdentity = "Noctra app (OAuth actor=app, static token)"
	}
	fmt.Printf("   Linear as:      %s\n", linearIdentity)
	if p.cfg.TriggerMode == "label" {
		fmt.Printf("   Watching:       label %q\n", p.cfg.TriggerLabel)
	} else {
		fmt.Printf("   Watching:       %q column\n", p.cfg.TriggerState)
	}
	fmt.Printf("   Agent:          %s\n", agentMode)
	fmt.Printf("   Review:         %s\n", reviewMode)
	fmt.Printf("   Auto-iterate:   %s\n", autoIterMode)
	fmt.Printf("   Release label:  %s\n", autoReleaseMode)
	planConfirmMode := "Disabled"
	if p.cfg.PlanConfirm {
		planConfirmMode = fmt.Sprintf("On (all tickets, label %q)", p.cfg.PlanConfirmLabel)
	} else if p.planConfirmLabelID != "" {
		planConfirmMode = fmt.Sprintf("Per-ticket (label %q)", p.cfg.PlanConfirmLabel)
	}
	fmt.Printf("   Sweep:          %s\n", sweepMode)
	fmt.Printf("   Plan-confirm:   %s\n", planConfirmMode)
	fmt.Printf("   Max concurrent: %d\n", p.cfg.MaxConcurrent)
	fmt.Printf("   Poll interval:  %s\n", p.cfg.PollInterval)
	fmt.Printf("   Agent timeout:  %s\n", p.cfg.AgentTimeout)
	fmt.Printf("   Max retries:    %d per ticket\n", p.cfg.MaxRetries)
	if p.cfg.MaxDispatches > 0 {
		fmt.Printf("   Max dispatches: %d per day (UTC)\n", p.cfg.MaxDispatches)
	} else {
		fmt.Printf("   Max dispatches: unlimited\n")
	}
	if p.dispatchPaused() {
		fmt.Printf("   Dispatch:       Paused by operator\n")
	} else {
		fmt.Printf("   Dispatch:       Running\n")
	}

	// Budget caps (ENG-217).
	budgetMode := "Unlimited"
	bs := p.budget.Stats()
	if bs.HasCaps() {
		parts := make([]string, 0, 2)
		if bs.MaxDailyTokens > 0 {
			parts = append(parts, budget.FormatTokens(bs.MaxDailyTokens)+" tokens/day")
		}
		if bs.MaxDailyUSD > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f/day", bs.MaxDailyUSD))
		}
		budgetMode = strings.Join(parts, ", ")
	}
	fmt.Printf("   Budget:         %s\n", budgetMode)

	rlMode := "pause (auto-resume after " + p.cfg.RateLimitCooldown.String() + ")"
	if p.cfg.RateLimitStrategy == "shutdown" {
		rlMode = "shutdown (legacy)"
	}
	fmt.Printf("   Rate limit:     %s\n", rlMode)

	fmt.Printf("   Notifications:  %s\n", notifyMode)

	if p.dash != nil {
		addr := normalizeDashboardAddr(p.cfg.DashboardAddr)
		fmt.Printf("   Dashboard:      http://%s/\n", addr)
		host, _, _ := net.SplitHostPort(addr)
		if host == "0.0.0.0" {
			fmt.Printf("   ⚠️  Dashboard bound to 0.0.0.0 — accessible from any network interface\n")
		}
	} else {
		fmt.Printf("   Dashboard:      Disabled\n")
	}

	fmt.Println()
	fmt.Println("Waiting for tickets... (Ctrl+C to stop)")
	fmt.Println()
}

func (p *Pipeline) summary(ctx context.Context) {
	p.mu.Lock()
	succ, fail := p.successCount, p.failCount
	p.mu.Unlock()
	dur := time.Since(p.sessionStart).Round(time.Minute)
	bs := p.budget.Stats()

	slog.Info("👋 session complete",
		"success", succ, "fail", fail, "duration", dur,
		"tokens", bs.SessionTokens, "cost_usd", bs.SessionCostUSD)

	usageLine := ""
	if bs.SessionTokens > 0 || bs.SessionCostUSD > 0 {
		usageLine = fmt.Sprintf("\n💰 Usage: %s tokens", budget.FormatTokens(bs.SessionTokens))
		if bs.SessionCostUSD > 0 {
			usageLine += fmt.Sprintf(" ($%.2f)", bs.SessionCostUSD)
		}
	}

	p.notifier.Send(ctx, fmt.Sprintf(
		"🌅 *Noctra session complete*\n✅ %d PRs created\n❌ %d failed\n⏱ Duration: %s%s",
		succ, fail, dur, usageLine))
}

// checkForUpdate runs once at startup in its own goroutine. It's strictly best-
// effort: it never blocks Run, swallows every error, and no-ops for dev builds
// (selfupdate.IsNewer returns false for empty/"dev"/"-dev"/"-snapshot"). When a
// newer release exists it logs a line and, if Telegram is wired up, pings once.
func (p *Pipeline) checkForUpdate(ctx context.Context) {
	// Skip dev/snapshot builds entirely — including "1.2.3-dev"/"-snapshot" — so
	// they don't make a pointless network call on every startup.
	if Version == "" || strings.Contains(Version, "dev") || strings.Contains(Version, "snapshot") {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	latest, err := selfupdate.Latest(cctx)
	if err != nil || !selfupdate.IsNewer(latest, Version) {
		return
	}
	slog.Info("🆙 a new version is available", "latest", latest, "current", Version,
		"run", "noctra update")
	p.notifier.Send(ctx, fmt.Sprintf(
		"🆙 *Noctra update available*\nA new version `%s` is out (running `%s`).\nRun `noctra update` to upgrade.",
		notify.EscapeMarkdown(latest), notify.EscapeMarkdown(Version)))
}

// startupCleanup is the lightweight version that runs on every boot — prune
// stale remotes and dead worktree entries in every known repo.
func (p *Pipeline) startupCleanup(ctx context.Context) {
	slog.Info("running startup cleanup")
	for _, rp := range p.resolver.AllRepoPaths() {
		_ = runIn(ctx, rp, "git", "fetch", "--prune")
		_ = runIn(ctx, rp, "git", "worktree", "prune")
	}
	slog.Info("startup cleanup done")
}
