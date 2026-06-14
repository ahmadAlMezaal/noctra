// Package budget tracks token and cost usage across agent runs, enforces
// configurable daily caps, and provides a pause/resume mechanism for the
// pipeline when caps or rate limits are hit.
package budget

import (
	"fmt"
	"sync"
	"time"
)

// Config holds the daily usage caps. Zero values mean unlimited.
type Config struct {
	MaxDailyTokens int64
	MaxDailyUSD    float64
}

// Stats is a snapshot of current usage and pause state, safe for display.
type Stats struct {
	SessionTokens  int64
	SessionCostUSD float64
	DailyTokens    int64
	DailyCostUSD   float64
	MaxDailyTokens int64
	MaxDailyUSD    float64
	Paused         bool
	PausedUntil    time.Time
	PauseReason    string
}

// HasCaps reports whether any budget cap is configured.
func (s Stats) HasCaps() bool {
	return s.MaxDailyTokens > 0 || s.MaxDailyUSD > 0
}

// Tracker tracks token/cost usage per session and per day, enforces daily caps,
// and provides a concurrency-safe pause/resume mechanism. It is always safe to
// use — when no caps are configured, Record and Exceeded are lightweight no-ops
// (Exceeded always returns false), and Pause/IsPaused remain functional for
// rate-limit pausing.
type Tracker struct {
	mu  sync.Mutex
	cfg Config

	sessionTokens  int64
	sessionCostUSD float64
	dailyTokens    int64
	dailyCostUSD   float64
	dayStart       time.Time

	paused      bool
	pausedUntil time.Time
	pauseReason string

	// now is a hook for testing — defaults to time.Now.
	now func() time.Time
}

// New returns a Tracker with the given caps. A zero Config is valid (no caps).
func New(cfg Config) *Tracker {
	return &Tracker{
		cfg:      cfg,
		dayStart: todayUTC(time.Now()),
		now:      time.Now,
	}
}

// Record adds tokens and cost from one agent run to both session and daily
// totals. Zero values (backend didn't report usage) are fine — they're a
// no-op on the cumulative counters.
func (t *Tracker) Record(tokens int64, costUSD float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maybeResetDaily()
	t.sessionTokens += tokens
	t.sessionCostUSD += costUSD
	t.dailyTokens += tokens
	t.dailyCostUSD += costUSD
}

// Exceeded reports whether any configured daily cap has been hit. Returns
// false when no caps are configured.
func (t *Tracker) Exceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maybeResetDaily()
	return t.exceeded()
}

func (t *Tracker) exceeded() bool {
	if t.cfg.MaxDailyTokens > 0 && t.dailyTokens >= t.cfg.MaxDailyTokens {
		return true
	}
	if t.cfg.MaxDailyUSD > 0 && t.dailyCostUSD >= t.cfg.MaxDailyUSD {
		return true
	}
	return false
}

// ExceededReason returns a human-readable explanation of which cap was hit,
// or "" if no cap is exceeded.
func (t *Tracker) ExceededReason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maybeResetDaily()
	if t.cfg.MaxDailyTokens > 0 && t.dailyTokens >= t.cfg.MaxDailyTokens {
		return fmt.Sprintf("daily token cap (%s/%s)",
			formatTokens(t.dailyTokens), formatTokens(t.cfg.MaxDailyTokens))
	}
	if t.cfg.MaxDailyUSD > 0 && t.dailyCostUSD >= t.cfg.MaxDailyUSD {
		return fmt.Sprintf("daily cost cap ($%.2f/$%.2f)",
			t.dailyCostUSD, t.cfg.MaxDailyUSD)
	}
	return ""
}

// Pause sets the tracker into a paused state with a reason and an optional
// auto-resume time. Pass a zero time for indefinite pause (e.g. manual resume).
func (t *Tracker) Pause(reason string, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paused = true
	t.pauseReason = reason
	t.pausedUntil = until
}

// IsPaused reports the current pause state. If the pausedUntil time has
// passed, it auto-resumes and returns false. This is the primary check the
// pipeline's poll loop calls before dispatching.
func (t *Tracker) IsPaused() (paused bool, until time.Time, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maybeResetDaily()
	if !t.paused {
		return false, time.Time{}, ""
	}
	// Auto-resume when the pause timer expires.
	if !t.pausedUntil.IsZero() && t.now().After(t.pausedUntil) {
		t.paused = false
		t.pauseReason = ""
		t.pausedUntil = time.Time{}
		return false, time.Time{}, ""
	}
	return true, t.pausedUntil, t.pauseReason
}

// Resume manually clears a pause. Intended for future /resume commands.
func (t *Tracker) Resume() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paused = false
	t.pauseReason = ""
	t.pausedUntil = time.Time{}
}

// Stats returns a point-in-time snapshot of usage and pause state.
func (t *Tracker) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maybeResetDaily()
	return Stats{
		SessionTokens:  t.sessionTokens,
		SessionCostUSD: t.sessionCostUSD,
		DailyTokens:    t.dailyTokens,
		DailyCostUSD:   t.dailyCostUSD,
		MaxDailyTokens: t.cfg.MaxDailyTokens,
		MaxDailyUSD:    t.cfg.MaxDailyUSD,
		Paused:         t.paused,
		PausedUntil:    t.pausedUntil,
		PauseReason:    t.pauseReason,
	}
}

// maybeResetDaily resets the daily counters when a new UTC day has started.
// Must be called with t.mu held.
func (t *Tracker) maybeResetDaily() {
	today := todayUTC(t.now())
	if today.After(t.dayStart) {
		t.dailyTokens = 0
		t.dailyCostUSD = 0
		t.dayStart = today
	}
}

// NextUTCMidnight returns the start of the next UTC day.
func NextUTCMidnight() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

// todayUTC returns midnight UTC of the given time's date.
func todayUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// formatTokens renders a token count in a compact human form (e.g. "1.5M",
// "250K", "1,234").
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		if n%1_000 == 0 {
			return fmt.Sprintf("%dK", n/1_000)
		}
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// FormatTokens is the exported version of formatTokens for use by callers
// that build display strings (banner, Telegram).
func FormatTokens(n int64) string { return formatTokens(n) }
