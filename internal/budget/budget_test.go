package budget

import (
	"testing"
	"time"
)

func TestRecord_AccumulatesUsage(t *testing.T) {
	tr := New(Config{})
	tr.Record(1000, 0.50)
	tr.Record(2000, 1.00)

	s := tr.Stats()
	if s.SessionTokens != 3000 {
		t.Errorf("SessionTokens: got %d, want 3000", s.SessionTokens)
	}
	if s.SessionCostUSD != 1.50 {
		t.Errorf("SessionCostUSD: got %f, want 1.50", s.SessionCostUSD)
	}
	if s.DailyTokens != 3000 {
		t.Errorf("DailyTokens: got %d, want 3000", s.DailyTokens)
	}
	if s.DailyCostUSD != 1.50 {
		t.Errorf("DailyCostUSD: got %f, want 1.50", s.DailyCostUSD)
	}
}

func TestExceeded_NoCaps(t *testing.T) {
	tr := New(Config{})
	tr.Record(999_999_999, 999_999.99)
	if tr.Exceeded() {
		t.Error("Exceeded should be false when no caps are configured")
	}
}

func TestExceeded_TokenCap(t *testing.T) {
	tr := New(Config{MaxDailyTokens: 5000})
	tr.Record(4999, 0)
	if tr.Exceeded() {
		t.Error("should not exceed at 4999/5000")
	}
	tr.Record(1, 0)
	if !tr.Exceeded() {
		t.Error("should exceed at 5000/5000")
	}
}

func TestExceeded_CostCap(t *testing.T) {
	tr := New(Config{MaxDailyUSD: 10.00})
	tr.Record(0, 9.99)
	if tr.Exceeded() {
		t.Error("should not exceed at $9.99/$10.00")
	}
	tr.Record(0, 0.01)
	if !tr.Exceeded() {
		t.Error("should exceed at $10.00/$10.00")
	}
}

func TestExceededReason(t *testing.T) {
	tr := New(Config{MaxDailyTokens: 1000, MaxDailyUSD: 5.00})
	if r := tr.ExceededReason(); r != "" {
		t.Errorf("expected empty reason, got %q", r)
	}
	tr.Record(1000, 0)
	if r := tr.ExceededReason(); r == "" {
		t.Error("expected non-empty reason for token cap")
	}
}

func TestPauseAndResume(t *testing.T) {
	tr := New(Config{})

	paused, _, _ := tr.IsPaused()
	if paused {
		t.Error("should not be paused initially")
	}

	until := time.Now().Add(1 * time.Hour)
	tr.Pause("rate limit", until)

	paused, gotUntil, reason := tr.IsPaused()
	if !paused {
		t.Error("should be paused after Pause()")
	}
	if reason != "rate limit" {
		t.Errorf("reason: got %q, want %q", reason, "rate limit")
	}
	if !gotUntil.Equal(until) {
		t.Errorf("until: got %v, want %v", gotUntil, until)
	}

	tr.Resume()
	paused, _, _ = tr.IsPaused()
	if paused {
		t.Error("should not be paused after Resume()")
	}
}

func TestIsPaused_AutoResume(t *testing.T) {
	tr := New(Config{})
	// Pause until a time that's already past.
	tr.Pause("test", time.Now().Add(-1*time.Second))

	paused, _, _ := tr.IsPaused()
	if paused {
		t.Error("should auto-resume when pausedUntil is in the past")
	}
}

func TestDailyReset(t *testing.T) {
	tr := New(Config{MaxDailyTokens: 1000})

	// Record usage and confirm it's tracked.
	tr.Record(999, 0)
	if tr.Exceeded() {
		t.Error("should not exceed at 999/1000")
	}

	// Simulate time advancing to the next day.
	tomorrow := time.Now().UTC().Add(25 * time.Hour)
	tr.now = func() time.Time { return tomorrow }

	// Daily counters should reset.
	if tr.Exceeded() {
		t.Error("should not exceed after daily reset")
	}
	s := tr.Stats()
	if s.DailyTokens != 0 {
		t.Errorf("DailyTokens after reset: got %d, want 0", s.DailyTokens)
	}
	// Session counters persist.
	if s.SessionTokens != 999 {
		t.Errorf("SessionTokens should persist across day boundary: got %d", s.SessionTokens)
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1K"},
		{1500, "1.5K"},
		{1_000_000, "1M"},
		{2_500_000, "2.5M"},
		{10_000_000, "10M"},
	}
	for _, tt := range tests {
		got := FormatTokens(tt.input)
		if got != tt.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNextUTCMidnight(t *testing.T) {
	m := NextUTCMidnight()
	now := time.Now().UTC()
	if !m.After(now) {
		t.Errorf("NextUTCMidnight() = %v, should be after now (%v)", m, now)
	}
	if m.Hour() != 0 || m.Minute() != 0 || m.Second() != 0 {
		t.Errorf("NextUTCMidnight() should be midnight, got %v", m)
	}
}

func TestStats_HasCaps(t *testing.T) {
	noCaps := Stats{}
	if noCaps.HasCaps() {
		t.Error("HasCaps should be false with no caps")
	}
	tokenCap := Stats{MaxDailyTokens: 1000}
	if !tokenCap.HasCaps() {
		t.Error("HasCaps should be true with token cap")
	}
	costCap := Stats{MaxDailyUSD: 10.0}
	if !costCap.HasCaps() {
		t.Error("HasCaps should be true with cost cap")
	}
}
