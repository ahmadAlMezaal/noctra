package sweep

import (
	"testing"
	"time"
)

func TestParseCron_Invalid(t *testing.T) {
	for _, expr := range []string{
		"",
		"0 0 * *",
		"0 0 * * * *",
		"60 0 * * *",
		"0 24 * * *",
		"0 0 0 * *",
		"0 0 32 * *",
		"0 0 * 13 *",
		"abc 0 * * *",
		"*/0 * * * *",
		"5-2 * * * *",
	} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q): expected error, got nil", expr)
		}
	}
}

func TestCronNext_DailyMidnight(t *testing.T) {
	s, err := ParseCron("0 0 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	from := time.Date(2026, 6, 20, 14, 30, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestCronNext_SpecificHour(t *testing.T) {
	s, _ := ParseCron("30 9 * * *")
	from := time.Date(2026, 6, 20, 9, 30, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 6, 21, 9, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next (strictly after) = %v, want %v", got, want)
	}
}

func TestCronNext_EveryFifteenMinutes(t *testing.T) {
	s, _ := ParseCron("*/15 * * * *")
	from := time.Date(2026, 6, 20, 10, 7, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 6, 20, 10, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestCronNext_Weekdays(t *testing.T) {
	s, _ := ParseCron("0 9 * * 1-5")
	// 2026-06-20 is a Saturday; next weekday 9am is Monday 2026-06-22.
	from := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v (weekday %s)", got, want, got.Weekday())
	}
}

func TestCronNext_DomDowOr(t *testing.T) {
	// When both dom and dow are restricted, either matching fires.
	s, _ := ParseCron("0 0 13 * 5") // the 13th OR any Friday
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := s.Next(from)
	// First Friday in June 2026 is the 5th — earlier than the 13th.
	want := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestCronNext_SundayAsSeven(t *testing.T) {
	s7, _ := ParseCron("0 0 * * 7")
	s0, _ := ParseCron("0 0 * * 0")
	from := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	if !s7.Next(from).Equal(s0.Next(from)) {
		t.Errorf("dow=7 and dow=0 should both mean Sunday: %v vs %v", s7.Next(from), s0.Next(from))
	}
}
