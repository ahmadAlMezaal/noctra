// Package agent runs a coding-agent CLI against a worktree and parses its session log for status signals (BLOCKED, rate limits, the final summary). The log appends across attempts, so callers record the file size BEFORE a run and read only the new tail (the "log_offset" pattern — see CLAUDE.md) to avoid re-detecting stale signals.
package agent

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// AttemptHeader writes a "--- Attempt <timestamp> ---" marker to the log.
func AttemptHeader(logFile string) error {
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "--- Attempt %s ---\n", time.Now().Format(time.RFC3339))
	return err
}

// OffsetBefore returns logFile's size in bytes; callers capture it BEFORE a run so parsing looks only at new content.
func OffsetBefore(logFile string) int64 {
	info, err := os.Stat(logFile)
	if err != nil {
		return 0
	}
	return info.Size()
}

// ReadAfter returns logFile's contents from offset to the end; a missing file or error reads as empty.
func ReadAfter(logFile string, offset int64) string {
	f, err := os.Open(logFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(f)
	return string(b)
}

// blockedRe matches the "BLOCKED: <reason>" line every backend's prompt asks for — backend-agnostic, unlike rate-limit detection (see HasRateLimit).
var blockedRe = regexp.MustCompile(`(?im)^BLOCKED:\s*(.*)$`)

// BlockedLine returns the first "BLOCKED: …" line in output, or "".
func BlockedLine(output string) string {
	m := blockedRe.FindString(output)
	return m
}

var noChangesRe = regexp.MustCompile(`(?im)^NO_CHANGES:\s*(.*)$`)

// NoChangesLine returns the reason from the last "NO_CHANGES: …" line, or ""; last match wins so an echoed instruction can't false-positive.
func NoChangesLine(output string) string {
	matches := noChangesRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ""
	}
	return strings.TrimSpace(matches[len(matches)-1][1])
}

// releaseRe matches the "RELEASE: <bump>" line the prompt asks for after the summary.
var releaseRe = regexp.MustCompile(`(?im)^RELEASE:\s*(.+)$`)

// ReleaseBump extracts the semver bump suggestion ("patch"/"minor"/"major"/"none", or "" if absent/unparseable); last match wins so an echoed instruction doesn't decide it.
func ReleaseBump(output string) string {
	matches := releaseRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ""
	}
	m := matches[len(matches)-1]
	val := strings.ToLower(strings.TrimSpace(m[1]))
	switch val {
	case "patch", "minor", "major", "none":
		return val
	}
	return ""
}

// ReleaseLabel returns the GitHub release label for a bump level, "" for "none", falling back to defaultBump when empty/unrecognized.
func ReleaseLabel(bump, defaultBump string) string {
	switch bump {
	case "none":
		return ""
	case "patch", "minor", "major":
		return "release:" + bump
	default:
		if defaultBump == "" {
			defaultBump = "patch"
		}
		return "release:" + defaultBump
	}
}

// Summary markers wrap the agent's final summary; extracting between them strips everything the CLI streams around it without guessing from line counts. ExtractSummary falls back to a last-N-lines heuristic when absent.
const (
	SummaryStartMarker = "===NOCTRA SUMMARY==="
	SummaryEndMarker   = "===END NOCTRA SUMMARY==="
)

// ExtractSummary returns the agent's final-attempt summary for the PR body: the text between the last Summary markers, or a DEBUG/footer-stripped 40-line tail as fallback.
func ExtractSummary(logContents string) string {
	const maxLines = 40

	// Scope to the last attempt so stale markers from an earlier one can't win.
	last := lastAttempt(logContents)

	if s, ok := betweenMarkers(last); ok {
		return s
	}

	var kept []string
	for _, line := range strings.Split(stripUsageFooter(last), "\n") {
		if strings.HasPrefix(line, "DEBUG: ") {
			continue
		}
		kept = append(kept, line)
	}

	for len(kept) > 0 && strings.TrimSpace(kept[0]) == "" {
		kept = kept[1:]
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) > maxLines {
		kept = kept[len(kept)-maxLines:]
	}
	return strings.Join(kept, "\n")
}

// lastAttempt returns logContents after the final "--- Attempt …" marker (the whole input if none).
func lastAttempt(logContents string) string {
	idx := strings.LastIndex(logContents, "--- Attempt ")
	if idx < 0 {
		return logContents
	}
	nl := strings.IndexByte(logContents[idx:], '\n')
	if nl < 0 {
		return ""
	}
	return logContents[idx+nl+1:]
}

// betweenMarkers returns the trimmed text between the Summary markers (false when absent); using the last start marker guards against an echoed instruction.
func betweenMarkers(s string) (string, bool) {
	return between(s, SummaryStartMarker, SummaryEndMarker)
}

// between returns the trimmed text between the last start marker and the next end marker; false when a marker is absent or the span empty.
func between(s, startMarker, endMarker string) (string, bool) {
	start := strings.LastIndex(s, startMarker)
	if start < 0 {
		return "", false
	}
	rest := s[start+len(startMarker):]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		return "", false
	}
	inner := strings.TrimSpace(rest[:end])
	if inner == "" {
		return "", false
	}
	return inner, true
}

// usageFooterRe matches a trailing token-usage footer (e.g. Codex's "tokens used: 167,195"); anchored to end so it never eats a mid-summary mention.
var usageFooterRe = regexp.MustCompile(`(?is)\n\s*tokens used\b.*$`)

// stripUsageFooter removes a trailing CLI usage footer if present.
func stripUsageFooter(s string) string {
	return usageFooterRe.ReplaceAllString(s, "")
}
