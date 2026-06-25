package pipeline

import (
	"context"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/budget"
	"github.com/ahmadAlMezaal/noctra/internal/config"
)

func TestSnapshot(t *testing.T) {
	p := &Pipeline{
		cfg:            &config.Config{},
		active:         map[string]struct{}{"ENG-1": {}, "ENG-2": {}},
		cancels:        map[string]context.CancelFunc{},
		killed:         map[string]struct{}{},
		failedAttempts: map[string]int{"ENG-3": 1, "ENG-1": 0},
		skipped:        map[string]struct{}{"ENG-4": {}},
		paused:         true,
		budget:         budget.New(budget.Config{MaxDailyTokens: 100_000}),
	}

	snap := p.Snapshot()

	// Active should contain ENG-1 and ENG-2.
	if len(snap.Active) != 2 {
		t.Fatalf("expected 2 active, got %d", len(snap.Active))
	}
	activeSet := map[string]bool{}
	for _, id := range snap.Active {
		activeSet[id] = true
	}
	if !activeSet["ENG-1"] || !activeSet["ENG-2"] {
		t.Errorf("active set = %v, want ENG-1 and ENG-2", snap.Active)
	}

	// Queued should contain ENG-3 (has failed attempts, not active, not skipped).
	// ENG-1 has failedAttempts=0 but is active, so excluded from queued.
	if len(snap.Queued) != 1 {
		t.Fatalf("expected 1 queued, got %d: %v", len(snap.Queued), snap.Queued)
	}
	if retries, ok := snap.Queued["ENG-3"]; !ok || retries != 1 {
		t.Errorf("queued = %v, want {ENG-3: 1}", snap.Queued)
	}

	// Skipped should contain ENG-4.
	if len(snap.Skipped) != 1 || snap.Skipped[0] != "ENG-4" {
		t.Errorf("skipped = %v, want [ENG-4]", snap.Skipped)
	}

	if !snap.Paused {
		t.Error("expected paused = true")
	}

	if snap.Budget.MaxDailyTokens != 100_000 {
		t.Errorf("budget max daily tokens = %d, want 100000", snap.Budget.MaxDailyTokens)
	}
}

func TestSnapshotEmpty(t *testing.T) {
	p := &Pipeline{
		cfg:            &config.Config{},
		active:         map[string]struct{}{},
		cancels:        map[string]context.CancelFunc{},
		killed:         map[string]struct{}{},
		failedAttempts: map[string]int{},
		skipped:        map[string]struct{}{},
		budget:         budget.New(budget.Config{}),
	}

	snap := p.Snapshot()

	if len(snap.Active) != 0 {
		t.Errorf("expected 0 active, got %d", len(snap.Active))
	}
	if len(snap.Queued) != 0 {
		t.Errorf("expected 0 queued, got %d", len(snap.Queued))
	}
	if len(snap.Skipped) != 0 {
		t.Errorf("expected 0 skipped, got %d", len(snap.Skipped))
	}
	if snap.Paused {
		t.Error("expected paused = false")
	}
}

func TestNormalizeDashboardAddr(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{":8080", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "0.0.0.0:8080"},
		{"127.0.0.1:9090", "127.0.0.1:9090"},
		{"localhost:3000", "localhost:3000"},
		{"invalid", "invalid"},
	}
	for _, tc := range tests {
		got := normalizeDashboardAddr(tc.in)
		if got != tc.want {
			t.Errorf("normalizeDashboardAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
