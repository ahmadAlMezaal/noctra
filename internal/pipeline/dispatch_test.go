package pipeline

import (
	"errors"
	"testing"

	"github.com/ahmadAlMezaal/nightshift/internal/repo"
)

func TestSkipPermanently_AddToSetAndRefundsDispatch(t *testing.T) {
	p := &Pipeline{
		skipped:        map[string]struct{}{},
		active:         map[string]struct{}{},
		failedAttempts: map[string]int{},
	}
	p.totalDispatches = 5

	p.skipPermanently("ENG-100")

	if _, ok := p.skipped["ENG-100"]; !ok {
		t.Fatal("ENG-100 should be in the skipped set")
	}
	if p.totalDispatches != 4 {
		t.Fatalf("totalDispatches: got %d, want 4 (should be refunded)", p.totalDispatches)
	}

	// Skipping again should be idempotent and not decrement again.
	p.skipPermanently("ENG-100")
	if p.totalDispatches != 4 {
		t.Fatalf("totalDispatches: got %d after second skip, want 4 (should be idempotent)", p.totalDispatches)
	}
}

func TestNonTransientError_Detectable(t *testing.T) {
	inner := errors.New("no repo is mapped for project \"Foo\"")
	nte := &repo.NonTransientError{Err: inner}

	// errors.As should find it.
	var target *repo.NonTransientError
	if !errors.As(nte, &target) {
		t.Fatal("errors.As should detect NonTransientError")
	}
	if target.Error() != inner.Error() {
		t.Errorf("Error(): got %q, want %q", target.Error(), inner.Error())
	}

	// Unwrap should return the inner error.
	if errors.Unwrap(nte) != inner {
		t.Fatal("Unwrap should return the inner error")
	}

	// A plain error should NOT match.
	plain := errors.New("transient clone failure")
	if errors.As(plain, &target) {
		t.Fatal("plain error should not match NonTransientError")
	}
}

func TestBumpFailed_BoundsRetries(t *testing.T) {
	p := &Pipeline{
		skipped:        map[string]struct{}{},
		active:         map[string]struct{}{},
		failedAttempts: map[string]int{},
	}

	for i := 1; i <= 5; i++ {
		got := p.bumpFailed("ENG-200")
		if got != i {
			t.Fatalf("bumpFailed attempt %d: got %d", i, got)
		}
	}
	if p.failCount != 5 {
		t.Fatalf("failCount: got %d, want 5", p.failCount)
	}
}
