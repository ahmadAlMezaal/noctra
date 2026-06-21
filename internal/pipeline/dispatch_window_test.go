package pipeline

import (
	"testing"
	"time"
)

// TestRollDispatchWindow_ResetsAtUTCMidnight locks in the daily-cap semantics
// (ENG-254): MAX_DISPATCHES counts per UTC day, so the counter and the
// once-per-day cap alert reset when the day rolls over — and only then.
func TestRollDispatchWindow_ResetsAtUTCMidnight(t *testing.T) {
	day1 := time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)

	p := &Pipeline{}

	// First roll establishes today's window.
	p.rollDispatchWindow(day1)
	p.totalDispatches = 7
	p.dispatchCapped = true

	// Same day, later — must NOT reset.
	p.rollDispatchWindow(day1.Add(8 * time.Hour))
	if p.totalDispatches != 7 || !p.dispatchCapped {
		t.Fatalf("same-day roll reset state: got count=%d capped=%v, want 7/true",
			p.totalDispatches, p.dispatchCapped)
	}

	// New UTC day — must reset the counter and clear the alert flag.
	p.rollDispatchWindow(day2)
	if p.totalDispatches != 0 {
		t.Fatalf("counter not reset at new day: got %d, want 0", p.totalDispatches)
	}
	if p.dispatchCapped {
		t.Fatal("cap alert flag not cleared at new day")
	}
}
