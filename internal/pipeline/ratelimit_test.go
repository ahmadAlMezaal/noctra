package pipeline

import (
	"errors"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
)

// TestRateLimited_OnlyOnFailure locks in the ENG-178 fix: a usage/rate limit is
// a failure mode, so it must only be classified on a FAILED run. A successful
// run whose transcript merely contains the words "rate limit" (e.g. the agent
// edited a file that documents rate-limit handling) must never be treated as
// rate-limited, or its completed work gets discarded.
func TestRateLimited_OnlyOnFailure(t *testing.T) {
	codex, err := agent.New("codex")
	if err != nil {
		t.Fatalf("New(codex): %v", err)
	}

	// Both of these match the backend's HasRateLimit regex.
	limitErr := "Error: rate limit reached"
	contentTrap := "{ h: \"Safe to leave alone\", p: \"...rate-limit detection...\" }" // ENG-178

	// Successful run (runErr == nil): never rate-limited, even with markers.
	if rateLimited(codex, nil, limitErr) {
		t.Error("successful run must not be classified as rate-limited")
	}
	if rateLimited(codex, nil, contentTrap) {
		t.Error("ENG-178 regression: successful run with 'rate-limit' in file content was flagged")
	}

	// Failed run: rate-limited only when the output carries the markers.
	runErr := errors.New("exit status 1")
	if !rateLimited(codex, runErr, limitErr) {
		t.Error("failed run with rate-limit markers should be classified as rate-limited")
	}
	if rateLimited(codex, runErr, "build failed: syntax error") {
		t.Error("failed run without markers should not be classified as rate-limited")
	}
}
