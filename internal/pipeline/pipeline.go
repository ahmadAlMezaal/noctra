// Package pipeline runs Noctra's main loop: poll Linear, dispatch a
// bounded worker pool of process_ticket goroutines, and shut down cleanly on
// signal, rate-limit, or dispatch cap.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/dashboard"
	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
	"github.com/ahmadAlMezaal/noctra/internal/notify"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/selfupdate"
	"github.com/ahmadAlMezaal/noctra/internal/state"
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
	resolver *repo.Resolver
	telegram *notify.Telegram
	review   *review.Gate
	agent    agent.Backend // selected coding-agent CLI (claude / codex)
	states   linear.StateIDs

	// Label-mode trigger — resolved at startup when cfg.TriggerMode == "label".
	labelID string

	// Auto-iterate plumbing — all nil when cfg.AutoIteratePRs is false.
	store   *state.Store
	gh      *github.Client
	watcher *watch.Watcher

	sessionStart time.Time

	mu                sync.Mutex
	active            map[string]struct{}           // identifiers in-flight
	cancels           map[string]context.CancelFunc // per-ticket cancel (for /kill)
	killed            map[string]struct{}           // tickets killed via /kill
	failedAttempts    map[string]int                // per-ticket retry counter
	skipped           map[string]struct{}           // non-transient failures — never re-dispatched
	totalDispatches   int
	successCount      int
	failCount         int
	rateLimitDetected bool
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

	p := &Pipeline{
		cfg:            cfg,
		linear:         newLinearClient(cfg),
		resolver:       repo.FromConfig(cfg),
		telegram:       notify.New(cfg.TelegramEnabled, cfg.TelegramBotToken, cfg.TelegramChatID),
		review:         review.NewWithMode(cfg.GeminiMode, cfg.GeminiAPIKey, cfg.GeminiModel),
		agent:          backend,
		active:         map[string]struct{}{},
		cancels:        map[string]context.CancelFunc{},
		killed:         map[string]struct{}{},
		failedAttempts: map[string]int{},
		skipped:        map[string]struct{}{},
		sessionStart:   time.Now(),
	}

	// Auto-iterate is opt-in. When disabled, store/gh/watcher stay nil and
	// the run loop never starts the PR-poller goroutine.
	if cfg.AutoIteratePRs {
		store, err := state.Open(cfg.StateFile)
		if err != nil {
			slog.Warn("auto-iterate: state store open failed; feature disabled",
				"path", cfg.StateFile, "err", err)
			return p
		}
		p.store = store
		p.gh = github.New()

		// Directive-only routing has no static repo registry: the watcher
		// discovers which repos to poll from the on-demand clones on disk,
		// re-read on every scan (the set grows as tickets are dispatched).
		p.watcher = watch.New(p.gh, store, p.resolver.AllRepoRemotes, cfg.TrustedReviewers)
	}

	return p
}

// Run blocks until ctx is canceled, the dispatch cap is hit, or a rate-limit
// is detected. It always waits for in-flight workers to finish before
// returning, and prints a session summary on the way out.
func (p *Pipeline) Run(ctx context.Context) error {
	for _, d := range []string{p.cfg.LogDir, p.cfg.WorktreeBase, p.cfg.ReposBase} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// In label mode the trigger-state ID isn't needed — pass "" so
	// ResolveStateIDs skips its validation. The in-review state is
	// still required for the post-PR transition.
	triggerStateName := p.cfg.TriggerState
	if p.cfg.TriggerMode == "label" {
		triggerStateName = ""
	}

	stateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	states, err := p.linear.ResolveStateIDs(stateCtx, p.cfg.LinearTeamKey, triggerStateName, p.cfg.InReviewState)
	cancel()
	if err != nil {
		return fmt.Errorf("resolve linear states: %w", err)
	}
	p.states = states

	if p.cfg.TriggerMode == "label" {
		labelCtx, labelCancel := context.WithTimeout(ctx, 30*time.Second)
		lid, err := p.linear.ResolveLabelID(labelCtx, p.cfg.TriggerLabel)
		labelCancel()
		if err != nil {
			return fmt.Errorf("resolve trigger label: %w", err)
		}
		p.labelID = lid
		slog.Info("resolved Linear label", "label", p.cfg.TriggerLabel, "id", lid)
	}

	slog.Info("resolved Linear states", "trigger_mode", p.cfg.TriggerMode, "in_review", p.cfg.InReviewState)

	p.startupCleanup(ctx)
	p.banner()
	if p.cfg.TriggerMode == "label" {
		p.telegram.Send(ctx, fmt.Sprintf("🌙 *Noctra started*\nWatching label \"%s\" for %s tickets",
			notify.EscapeMarkdown(p.cfg.TriggerLabel), notify.EscapeMarkdown(p.cfg.LinearTeamKey)))
	} else {
		p.telegram.Send(ctx, fmt.Sprintf("🌙 *Noctra started*\nWatching \"%s\" for %s tickets",
			notify.EscapeMarkdown(p.cfg.TriggerState), notify.EscapeMarkdown(p.cfg.LinearTeamKey)))
	}

	// Best-effort update check: never blocks startup, swallows all errors, and
	// does nothing for dev builds (IsNewer returns false). Logs a line and
	// pings Telegram once if a newer release is published.
	go p.checkForUpdate(ctx)

	var wg sync.WaitGroup

	// Start the Telegram command listener alongside the poll loop if configured.
	// The listener shares the WaitGroup so shutdown drains it like any other goroutine.
	if p.cfg.TelegramEnabled && p.cfg.TelegramBotToken != "" && p.cfg.TelegramChatID != "" {
		listener := telegram.New(p.cfg.TelegramBotToken, p.cfg.TelegramChatID)
		p.registerCommands(listener.Dispatcher())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := listener.Run(ctx); err != nil {
				slog.Warn("telegram listener stopped", "err", err)
			}
		}()
		slog.Info("telegram command listener started")
	}

	// Start the dashboard HTTP server if configured. Runs in its own
	// goroutine on the shared WaitGroup so shutdown drains it cleanly.
	var dashSrv *dashboard.Server
	if p.cfg.DashboardAddr != "" {
		dashSrv = dashboard.New(p, p, p.cfg.DashboardToken, p.cfg.LogDir)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := dashSrv.ListenAndServe(p.cfg.DashboardAddr); err != nil {
				// http.ErrServerClosed is expected on graceful shutdown.
				if err.Error() != "http: Server closed" {
					slog.Warn("dashboard server stopped", "err", err)
				}
			}
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
		go p.runWatcher(ctx, &wg)
	}

	// shutdownDashboard gracefully stops the dashboard server if it was started.
	shutdownDashboard := func() {
		if dashSrv != nil {
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutCancel()
			_ = dashSrv.Shutdown(shutCtx)
		}
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("🌅 shutting down — waiting for active tasks")
			shutdownDashboard()
			wg.Wait()
			p.summary(ctx)
			return nil

		case <-ticker.C:
			p.mu.Lock()
			rlDetected := p.rateLimitDetected
			atCap := p.totalDispatches >= p.cfg.MaxDispatches
			p.mu.Unlock()

			if rlDetected {
				slog.Info("🛑 rate limit detected — shutting down")
				shutdownDashboard()
				wg.Wait()
				p.summary(ctx)
				return nil
			}
			if atCap {
				slog.Info("🛑 max dispatches reached — shutting down",
					"limit", p.cfg.MaxDispatches)
				shutdownDashboard()
				wg.Wait()
				p.summary(ctx)
				return nil
			}
			p.pollOnce(ctx, &wg)
		}
	}
}

func (p *Pipeline) pollOnce(ctx context.Context, wg *sync.WaitGroup) {
	p.mu.Lock()
	inFlight := len(p.active)
	available := p.cfg.MaxConcurrent - inFlight
	dispatched := p.totalDispatches
	p.mu.Unlock()

	if dispatched >= p.cfg.MaxDispatches {
		return
	}
	if available <= 0 {
		slog.Debug("at capacity", "active", inFlight, "max", p.cfg.MaxConcurrent)
		return
	}

	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	var (
		issues []linear.Issue
		err    error
	)
	if p.cfg.TriggerMode == "label" {
		issues, err = p.linear.FetchLabeledIssues(fctx, p.cfg.TriggerLabel)
	} else {
		issues, err = p.linear.FetchTriggerIssues(fctx, p.cfg.TriggerState)
	}
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
		if p.totalDispatches >= p.cfg.MaxDispatches {
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
		ticketCtx, ticketCancel := context.WithCancel(ctx)
		p.active[issue.Identifier] = struct{}{}
		p.cancels[issue.Identifier] = ticketCancel
		p.totalDispatches++
		p.mu.Unlock()

		available--

		slog.Info("🎯 dispatching", "id", issue.Identifier, "title", issue.Title)

		wg.Add(1)
		go func(iss linear.Issue) {
			defer wg.Done()
			defer p.markDone(iss.Identifier)
			p.process(ticketCtx, iss)
		}(issue)
	}
}

func (p *Pipeline) markDone(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, id)
	if cancel, ok := p.cancels[id]; ok {
		cancel() // release context resources
		delete(p.cancels, id)
	}
	delete(p.killed, id)
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

func (p *Pipeline) bumpFailed(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failedAttempts[id]++
	p.failCount++
	return p.failedAttempts[id]
}

func (p *Pipeline) bumpSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.successCount++
}

// skipPermanently marks a ticket as permanently skipped (non-transient failure
// like a project with no `Repo:` directive and no REPO_PATH fallback). The
// ticket won't be re-dispatched on future polls, and the dispatch it consumed
// is refunded so deterministic config errors don't burn the dispatch budget or
// shut the agent down.
// Idempotent: calling multiple times for the same ID only refunds once.
func (p *Pipeline) skipPermanently(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.skipped[id]; !ok {
		p.skipped[id] = struct{}{}
		p.totalDispatches-- // refund — config errors shouldn't count
	}
}

func (p *Pipeline) flagRateLimit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rateLimitDetected = true
}

// DashboardStatus returns a snapshot of the pipeline state for the dashboard.
// Implements dashboard.Snapshot.
func (p *Pipeline) DashboardStatus() dashboard.Status {
	p.mu.Lock()
	active := make([]string, 0, len(p.active))
	for id := range p.active {
		active = append(active, id)
	}
	killed := make([]string, 0, len(p.killed))
	for id := range p.killed {
		killed = append(killed, id)
	}
	skipped := make([]string, 0, len(p.skipped))
	for id := range p.skipped {
		skipped = append(skipped, id)
	}
	failed := make(map[string]int, len(p.failedAttempts))
	for id, n := range p.failedAttempts {
		failed[id] = n
	}
	stats := dashboard.SessionStats{
		TotalDispatches: p.totalDispatches,
		MaxDispatches:   p.cfg.MaxDispatches,
		SuccessCount:    p.successCount,
		FailCount:       p.failCount,
	}
	maxW := p.cfg.MaxConcurrent
	p.mu.Unlock()

	var prState map[string]state.PRState
	if p.store != nil {
		prState = p.store.All()
	}

	return dashboard.Status{
		Active:     active,
		Killed:     killed,
		Skipped:    skipped,
		Failed:     failed,
		Stats:      stats,
		PRState:    prState,
		MaxWorkers: maxW,
		Uptime:     time.Since(p.sessionStart).Round(time.Second).String(),
	}
}

// Requeue moves a ticket back to the trigger state/label. Implements
// dashboard.Controller. If extraContext is non-empty, it is posted as a
// Linear comment before the state transition.
func (p *Pipeline) Requeue(ctx context.Context, identifier, extraContext string) error {
	issue, err := p.linear.GetIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Errorf("ticket %s not found: %w", identifier, err)
	}

	if extraContext != "" {
		comment := fmt.Sprintf("**Requeued via dashboard**\n\n%s", extraContext)
		if err := p.linear.Comment(ctx, issue.ID, comment); err != nil {
			return fmt.Errorf("comment on %s failed: %w", identifier, err)
		}
	}

	if p.cfg.TriggerMode == "label" && p.labelID != "" {
		if err := p.linear.AddLabel(ctx, issue.ID, p.labelID); err != nil {
			return fmt.Errorf("add trigger label on %s failed: %w", identifier, err)
		}
	} else if p.states.Trigger != "" {
		if err := p.linear.SetState(ctx, issue.ID, p.states.Trigger); err != nil {
			return fmt.Errorf("set trigger state on %s failed: %w", identifier, err)
		}
	}
	return nil
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

// newLinearClient picks the Linear auth: an `actor=app` OAuth token (so Noctra
// posts under its own app identity) when configured, otherwise the personal API
// key. The OAuth token, when set, is used for every Linear call.
func newLinearClient(cfg *config.Config) *linear.Client {
	if cfg.LinearOAuthToken != "" {
		return linear.NewOAuth(cfg.LinearOAuthToken)
	}
	return linear.New(cfg.LinearAPIKey)
}

func (p *Pipeline) banner() {
	reviewMode := "Disabled"
	if p.review.Enabled() {
		reviewMode = fmt.Sprintf("Gemini %s (%s)", p.review.Mode, p.review.Model)
	}
	notifyMode := "Disabled"
	if p.telegram.Enabled {
		notifyMode = "Telegram"
	}
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

	// Repos are routed per-ticket from each Linear project's "Repo:" directive
	// and cloned on demand, so there's no static list at startup — report the
	// routing mode plus however many clones already exist on disk.
	repoSummary := "Linear project Repo: directives"
	if n := len(p.resolver.AllRepoPaths()); n > 0 {
		repoSummary += fmt.Sprintf(" (%d cloned)", n)
	}
	if p.cfg.RepoPath != "" {
		repoSummary += " + REPO_PATH fallback"
	}

	fmt.Println()
	fmt.Println("🌙 Noctra (Go)")
	fmt.Printf("   Repos:          %s\n", repoSummary)
	fmt.Printf("   Worktrees:      %s\n", p.cfg.WorktreeBase)
	fmt.Printf("   Team:           %s\n", p.cfg.LinearTeamKey)
	linearIdentity := "personal API key"
	if p.cfg.LinearOAuthToken != "" {
		linearIdentity = "Noctra app (OAuth actor=app)"
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
	fmt.Printf("   Max concurrent: %d\n", p.cfg.MaxConcurrent)
	fmt.Printf("   Poll interval:  %s\n", p.cfg.PollInterval)
	fmt.Printf("   Agent timeout:  %s\n", p.cfg.AgentTimeout)
	fmt.Printf("   Max retries:    %d per ticket\n", p.cfg.MaxRetries)
	fmt.Printf("   Max dispatches: %d per session\n", p.cfg.MaxDispatches)
	fmt.Printf("   Notifications:  %s\n", notifyMode)
	if p.cfg.DashboardAddr != "" {
		fmt.Printf("   Dashboard:      http://%s\n", p.cfg.DashboardAddr)
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
	slog.Info("👋 session complete", "success", succ, "fail", fail, "duration", dur)
	p.telegram.Send(ctx, fmt.Sprintf(
		"🌅 *Noctra session complete*\n✅ %d PRs created\n❌ %d failed\n⏱ Duration: %s",
		succ, fail, dur))
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
	p.telegram.Send(ctx, fmt.Sprintf(
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
