package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOffsetBefore_AndReadAfter_OnlyExposesNewContent(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "log.txt")

	// First attempt's output
	first := "--- Attempt 2024-01-01T00:00:00 ---\nDEBUG: pwd = /repo\nBLOCKED: Wrong repository\n"
	if err := os.WriteFile(logFile, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}

	offset := OffsetBefore(logFile)

	// Second attempt appends — clean run, no BLOCKED.
	more := "--- Attempt 2024-01-01T01:00:00 ---\nDEBUG: pwd = /repo\nAll done.\n"
	f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(more)
	_ = f.Close()

	tail := ReadAfter(logFile, offset)
	if BlockedLine(tail) != "" {
		t.Errorf("BLOCKED from old attempt should not be detected, got %q", BlockedLine(tail))
	}
	if HasRateLimit(tail) {
		t.Errorf("rate-limit should not be detected in clean tail")
	}
}

func TestBlockedLine_DetectsNewBlocked(t *testing.T) {
	tail := "DEBUG: pwd = /repo\nBLOCKED: Missing API credentials\n"
	got := BlockedLine(tail)
	if !strings.Contains(got, "Missing API credentials") {
		t.Errorf("BlockedLine: got %q", got)
	}
}

func TestHasRateLimit(t *testing.T) {
	cases := map[string]bool{
		"All good":                                     false,
		"Error: rate limit exceeded":                   true,
		"Error: too many requests":                     true,
		"Hit a usage limit on the API":                 true,
		"You have exceeded the daily request limit":    true,
		"nothing wrong here, just chatting about apis": false,
	}
	for in, want := range cases {
		if got := HasRateLimit(in); got != want {
			t.Errorf("HasRateLimit(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExtractSummary_StripsDebugAndKeepsLastAttempt(t *testing.T) {
	log := `--- Attempt 2024-01-01T00:00:00 ---
First attempt that should be ignored.
--- Attempt 2024-01-01T01:00:00 ---
DEBUG: pwd = /repo
DEBUG: branch = nightshift/eng-42
Here is the summary of changes.
Added a new feature.
`
	got := ExtractSummary(log)
	if strings.Contains(got, "DEBUG:") {
		t.Errorf("summary should not contain DEBUG lines, got:\n%s", got)
	}
	if strings.Contains(got, "First attempt") {
		t.Errorf("summary should not contain old attempt content, got:\n%s", got)
	}
	if strings.Contains(got, "--- Attempt") {
		t.Errorf("summary should not contain attempt markers, got:\n%s", got)
	}
	if !strings.Contains(got, "Added a new feature") {
		t.Errorf("summary should contain real content, got:\n%s", got)
	}
}
