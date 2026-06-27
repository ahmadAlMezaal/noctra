package pipeline

import (
	"errors"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/agent"
)

// TestRateLimited_OnlyOnFailure locks in ENG-178: a rate limit is classified only on a FAILED run — a successful run merely mentioning "rate limit" must not be, or its work gets discarded.
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

func TestClassifyAgentRun_FixPassTimeoutAndRateLimit(t *testing.T) {
	codex, err := agent.New("codex")
	if err != nil {
		t.Fatalf("New(codex): %v", err)
	}

	if got := classifyAgentRun(codex, agent.ErrTimedOut, ""); got != agentRunTimedOut {
		t.Fatalf("timeout classify = %v, want %v", got, agentRunTimedOut)
	}
	if got := classifyAgentRun(codex, errors.New("exit status 1"), "Error: rate limit reached"); got != agentRunRateLimited {
		t.Fatalf("rate limit classify = %v, want %v", got, agentRunRateLimited)
	}
	if got := classifyAgentRun(codex, errors.New("exit status 1"), "syntax error"); got != agentRunFailed {
		t.Fatalf("generic failure classify = %v, want %v", got, agentRunFailed)
	}
	if got := classifyAgentRun(codex, nil, "Error: rate limit reached"); got != agentRunOK {
		t.Fatalf("successful transcript classify = %v, want %v", got, agentRunOK)
	}
}
