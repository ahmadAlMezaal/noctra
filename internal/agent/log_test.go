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
	if (claudeBackend{}).HasRateLimit(tail) {
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

func TestExtractSummary_StripsDebugAndKeepsLastAttempt(t *testing.T) {
	log := `--- Attempt 2024-01-01T00:00:00 ---
First attempt that should be ignored.
--- Attempt 2024-01-01T01:00:00 ---
DEBUG: pwd = /repo
DEBUG: branch = noctra/eng-42
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

func TestExtractSummary_PrefersMarkerDelimitedSummary(t *testing.T) {
	// Simulates a Codex run: the CLI streams the diff and a usage footer around
	// the marker-wrapped summary. Only the marked content should survive.
	log := "--- Attempt 2024-01-01T01:00:00 ---\n" +
		"DEBUG: pwd = /repo\n" +
		"+func Foo() {}\n" +
		"-old line\n" +
		"@@ -1,2 +1,2 @@\n" +
		SummaryStartMarker + "\n" +
		"Implemented ENG-177.\nAdded GEMINI_MODE=api|cli.\n" +
		SummaryEndMarker + "\n" +
		"tokens used\n167,195\n"

	got := ExtractSummary(log)
	want := "Implemented ENG-177.\nAdded GEMINI_MODE=api|cli."
	if got != want {
		t.Errorf("marker extraction:\n got: %q\nwant: %q", got, want)
	}
	for _, leak := range []string{"func Foo", "@@", "tokens used", "167,195", "DEBUG:"} {
		if strings.Contains(got, leak) {
			t.Errorf("summary leaked %q:\n%s", leak, got)
		}
	}
}

func TestExtractSummary_UsesLastMarkerPair(t *testing.T) {
	// The agent may echo the instruction (including the markers) earlier in its
	// output; the actual summary is the LAST pair.
	log := SummaryStartMarker + "\n<your summary here>\n" + SummaryEndMarker + "\n" +
		"...work...\n" +
		SummaryStartMarker + "\nThe real summary.\n" + SummaryEndMarker + "\n"
	if got := ExtractSummary(log); got != "The real summary." {
		t.Errorf("should use last marker pair, got: %q", got)
	}
}

func TestExtractSummary_FallbackStripsUsageFooter(t *testing.T) {
	// No markers (older log / non-compliant agent): the heuristic still drops a
	// trailing Codex-style usage footer.
	log := "--- Attempt 2024-01-01T01:00:00 ---\n" +
		"DEBUG: pwd = /repo\n" +
		"Implemented the feature.\n" +
		"tokens used: 167,195\n"
	got := ExtractSummary(log)
	if !strings.Contains(got, "Implemented the feature") {
		t.Errorf("fallback should keep real content, got: %q", got)
	}
	if strings.Contains(got, "tokens used") || strings.Contains(got, "167,195") {
		t.Errorf("fallback should strip usage footer, got: %q", got)
	}
}
