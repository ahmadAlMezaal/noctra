// Package pipeline runs Nightshift's main loop: poll Linear, dispatch a
// bounded worker pool of process_ticket goroutines, and shut down cleanly on
// signal, rate-limit, or dispatch cap.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ahmadAlMezaal/nightshift/internal/config"
	"github.com/ahmadAlMezaal/nightshift/internal/linear"
	"github.com/ahmadAlMezaal/nightshift/internal/notify"
	"github.com/ahmadAlMezaal/nightshift/internal/repo"
	"github.com/ahmadAlMezaal/nightshift/internal/review"
)

// Pipeline holds the wiring shared by every ticket the agent processes.
type Pipeline struct {
	cfg      *config.Config
	linear   *linear.Client
	resolver *repo.Resolver
	telegram *notify.Telegram
	review   *review.Gate
	states   linear.StateIDs

	sessionStart time.Time

	mu                sync.Mutex
	active            map[string]struct{} // identifiers in-flight
	failedAttempts    map[string]int      // per-ticket retry counter
	totalDispatches   int
	successCount      int
	failCount         int
	rateLimitDetected bool
}

// New constructs a Pipeline. It does not perform any I/O — call Run to start.
func New(cfg *config.Config) *Pipeline {
	return &Pipeline{
		cfg:            cfg,
		linear:         linear.New(cfg.LinearAPIKey),
		resolver:       repo.FromConfig(cfg),
		telegram:       notify.New(cfg.TelegramEnabled, cfg.TelegramBotToken, cfg.TelegramChatID),
		review:         review.New(cfg.GeminiAPIKey, cfg.GeminiModel),
		active:         map[string]struct{}{},
		failedAttempts: map[string]int{},
		sessionStart:   time.Now(),
	}
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

	stateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	states, err := p.linear.ResolveStateIDs(stateCtx, p.cfg.LinearTeamKey, p.cfg.TriggerState, p.cfg.InReviewState)
	cancel()
	if err != nil {
		return fmt.Errorf("resolve linear states: %w", err)
	}
	p.states = states
	slog.Info("resolved Linear states", "trigger", p.cfg.TriggerState, "in_review", p.cfg.InReviewState)

	p.startupCleanup(ctx)
	p.banner()
	p.telegram.Send(ctx, fmt.Sprintf("🌙 *Nightshift started*\nWatching %q for %s tickets",
		p.cfg.TriggerState, p.cfg.LinearTeamKey))

	var wg sync.WaitGroup
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	// One poll right away so we don't sit idle for the first interval.
	p.pollOnce(ctx, &wg)

	for {
		select {
		case <-ctx.Done():
			slog.Info("🌅 shutting down — waiting for active tasks")
			wg.Wait()
			p.summary(ctx)
			return nil

		case <-ticker.C:
			p.mu.Lock()
			rateLimited := p.rateLimitDetected
			atCap := p.totalDispatches >= p.cfg.MaxDispatches
			p.mu.Unlock()

			if rateLimited {
				slog.Info("🛑 rate limit detected — shutting down")
				wg.Wait()
				p.summary(ctx)
				return nil
			}
			if atCap {
				slog.Info("🛑 max dispatches reached — shutting down",
					"limit", p.cfg.MaxDispatches)
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
	issues, err := p.linear.FetchTriggerIssues(fctx, p.cfg.TriggerState)
	cancel()
	if err != nil {
		slog.Warn("fetch tickets failed", "err", err)
		return
	}

	slog.Info("poll",
		"trigger", p.cfg.TriggerState,
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
		p.active[issue.Identifier] = struct{}{}
		p.totalDispatches++
		p.mu.Unlock()

		available--

		slog.Info("🎯 dispatching", "id", issue.Identifier, "title", issue.Title)

		wg.Add(1)
		go func(iss linear.Issue) {
			defer wg.Done()
			defer p.markDone(iss.Identifier)
			p.process(ctx, iss)
		}(issue)
	}
}

func (p *Pipeline) markDone(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, id)
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

func (p *Pipeline) flagRateLimit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rateLimitDetected = true
}

func (p *Pipeline) banner() {
	reviewMode := "Disabled"
	if p.review.Enabled() {
		reviewMode = fmt.Sprintf("Gemini (%s)", p.cfg.GeminiModel)
	}
	notifyMode := "Disabled"
	if p.telegram.Enabled {
		notifyMode = "Telegram"
	}

	repoSummary := p.cfg.RepoPath
	if p.cfg.Registry != nil {
		count := len(p.cfg.Registry.Repos)
		repoSummary = fmt.Sprintf("%d registered (repos.json)", count)
		if p.cfg.RepoPath != "" {
			repoSummary += " + REPO_PATH fallback"
		}
	}

	fmt.Println()
	fmt.Println("🌙 Nightshift (Go)")
	fmt.Printf("   Repos:          %s\n", repoSummary)
	fmt.Printf("   Worktrees:      %s\n", p.cfg.WorktreeBase)
	fmt.Printf("   Team:           %s\n", p.cfg.LinearTeamKey)
	fmt.Printf("   Watching:       %q column\n", p.cfg.TriggerState)
	fmt.Printf("   Review:         %s\n", reviewMode)
	fmt.Printf("   Max concurrent: %d\n", p.cfg.MaxConcurrent)
	fmt.Printf("   Poll interval:  %s\n", p.cfg.PollInterval)
	fmt.Printf("   Agent timeout:  %s\n", p.cfg.AgentTimeout)
	fmt.Printf("   Max retries:    %d per ticket\n", p.cfg.MaxRetries)
	fmt.Printf("   Max dispatches: %d per session\n", p.cfg.MaxDispatches)
	fmt.Printf("   Notifications:  %s\n", notifyMode)
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
		"🌅 *Nightshift session complete*\n✅ %d PRs created\n❌ %d failed\n⏱ Duration: %s",
		succ, fail, dur))
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
