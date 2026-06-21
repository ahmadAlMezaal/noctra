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

func TestNoChangesLine_DetectsReasonAndTrims(t *testing.T) {
	tail := "DEBUG: pwd = /repo\nNO_CHANGES: Site already documents v0.21.1\n"
	if got := NoChangesLine(tail); got != "Site already documents v0.21.1" {
		t.Errorf("NoChangesLine: got %q", got)
	}
	if NoChangesLine("no marker here") != "" {
		t.Error("NoChangesLine should be empty without a marker")
	}
}

func TestNoChangesLine_UsesLastMatch(t *testing.T) {
	// An echoed prompt instruction precedes the agent's real answer; the last
	// match must win so the echo doesn't decide the outcome.
	out := "say NO_CHANGES: <reason> and stop\n...work...\nNO_CHANGES: Already done\n"
	if got := NoChangesLine(out); got != "Already done" {
		t.Errorf("NoChangesLine: got %q, want last match %q", got, "Already done")
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

func TestReleaseBump(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"patch lowercase", "some output\nRELEASE: patch\n", "patch"},
		{"minor lowercase", "RELEASE: minor\n", "minor"},
		{"major lowercase", "RELEASE: major\n", "major"},
		{"none", "RELEASE: none\n", "none"},
		{"case insensitive key", "release: minor\n", "minor"},
		{"case insensitive value", "RELEASE: MINOR\n", "minor"},
		{"mixed case", "Release: Patch\n", "patch"},
		{"with leading spaces in value", "RELEASE:   patch  \n", "patch"},
		{"mid-output", "line one\nRELEASE: major\nline three\n", "major"},
		{"missing", "no release line here\n", ""},
		{"unparseable value", "RELEASE: yolo\n", ""},
		{"empty value", "RELEASE: \n", ""},
		{"after summary markers", SummaryStartMarker + "\nstuff\n" + SummaryEndMarker + "\nRELEASE: minor\n", "minor"},
		{"echoed prompt then real answer", "End with RELEASE: patch | minor | major | none\n...\nRELEASE: minor\n", "minor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReleaseBump(tt.output)
			if got != tt.want {
				t.Errorf("ReleaseBump(%q) = %q, want %q", tt.output, got, tt.want)
			}
		})
	}
}

func TestReleaseLabel(t *testing.T) {
	tests := []struct {
		name        string
		bump        string
		defaultBump string
		want        string
	}{
		{"patch", "patch", "patch", "release:patch"},
		{"minor", "minor", "patch", "release:minor"},
		{"major", "major", "patch", "release:major"},
		{"none skips", "none", "patch", ""},
		{"empty falls back to default patch", "", "patch", "release:patch"},
		{"empty falls back to default minor", "", "minor", "release:minor"},
		{"unknown falls back to default", "invalid", "major", "release:major"},
		{"empty default falls back to patch", "", "", "release:patch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReleaseLabel(tt.bump, tt.defaultBump)
			if got != tt.want {
				t.Errorf("ReleaseLabel(%q, %q) = %q, want %q", tt.bump, tt.defaultBump, got, tt.want)
			}
		})
	}
}
