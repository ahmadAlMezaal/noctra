// Package agent runs Claude Code against a worktree and parses the resulting
// session log for status signals (BLOCKED, rate limits, the final summary).
//
// The log file at .agent-logs/<IDENTIFIER>.log appends across attempts. To
// avoid mis-detecting BLOCKED or rate-limit strings from a previous attempt,
// callers record the file's size BEFORE running the agent, then read only the
// new tail. This is the "log_offset" pattern carried over from the bash
// version — see CLAUDE.md.
package agent

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// AttemptHeader writes a "--- Attempt <timestamp> ---" marker to the log,
// matching the format the bash predecessor used.
func AttemptHeader(logFile string) error {
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "--- Attempt %s ---\n", time.Now().Format(time.RFC3339))
	return err
}

// OffsetBefore returns the current size of logFile in bytes. Callers capture
// this BEFORE invoking the agent so that subsequent log parsing can look only
// at the new content.
func OffsetBefore(logFile string) int64 {
	info, err := os.Stat(logFile)
	if err != nil {
		return 0
	}
	return info.Size()
}

// ReadAfter returns the contents of logFile from the given byte offset to the
// end. A missing file or error reads as empty (the caller's status checks are
// substring searches and tolerate that).
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

// blockedRe matches the "BLOCKED: <reason>" line every backend's prompt asks
// the agent to print when it needs human input — so it is backend-agnostic.
// Rate-limit detection, by contrast, depends on the CLI's error phrasing and
// lives on each Backend (see HasRateLimit).
var blockedRe = regexp.MustCompile(`(?im)^BLOCKED:\s*(.*)$`)

// BlockedLine returns the first "BLOCKED: …" line in output (case-insensitive
// at the start of a line) or "" if none.
func BlockedLine(output string) string {
	m := blockedRe.FindString(output)
	return m
}

var noChangesRe = regexp.MustCompile(`(?im)^NO_CHANGES:\s*(.*)$`)

// NoChangesLine returns the reason from the last "NO_CHANGES: …" line, or "".
// The last match wins (mirroring ReleaseBump) so an echoed prompt instruction
// or a draft earlier in the log can't trigger a false positive.
func NoChangesLine(output string) string {
	matches := noChangesRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ""
	}
	return strings.TrimSpace(matches[len(matches)-1][1])
}

// releaseRe matches the "RELEASE: <bump>" line the prompt asks the agent to
// emit after its summary. Case-insensitive, anchored to start of line.
var releaseRe = regexp.MustCompile(`(?im)^RELEASE:\s*(.+)$`)

// ReleaseBump extracts the semver bump suggestion from the agent output.
// When multiple matches exist (e.g. the agent echoes the prompt instruction
// before emitting its own RELEASE: line), the last match wins — mirroring the
// betweenMarkers strategy for summary extraction.
// Returns one of "patch", "minor", "major", "none", or "" (not found / unparseable).
func ReleaseBump(output string) string {
	matches := releaseRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ""
	}
	// Use the last match — the agent's actual answer, not an echoed instruction.
	m := matches[len(matches)-1]
	val := strings.ToLower(strings.TrimSpace(m[1]))
	switch val {
	case "patch", "minor", "major", "none":
		return val
	}
	return ""
}

// ReleaseLabel returns the GitHub label name for a given bump level, or ""
// when the bump is "none" (intentionally skip release). An empty/unrecognized
// bump falls back to defaultBump.
func ReleaseLabel(bump, defaultBump string) string {
	switch bump {
	case "none":
		return ""
	case "patch", "minor", "major":
		return "release:" + bump
	default:
		// Missing or unparseable — use the configured default.
		if defaultBump == "" {
			defaultBump = "patch"
		}
		return "release:" + defaultBump
	}
}

// Summary markers the prompt asks the agent to wrap its final summary in (see
// BuildPrompt). Extracting between these is backend-agnostic and deterministic:
// it strips everything the CLI streams around the summary — Codex's diff dump
// and "tokens used" footer, Claude's preamble — without guessing from line
// counts. ExtractSummary falls back to the last-N-lines heuristic when the
// markers are absent (older logs, or an agent that didn't comply).
const (
	SummaryStartMarker = "===NOCTRA SUMMARY==="
	SummaryEndMarker   = "===END NOCTRA SUMMARY==="
)

// ExtractSummary returns the agent's final-attempt summary for the PR body.
//
// Preferred path: the content between the last SummaryStartMarker/SummaryEndMarker
// pair the agent printed. Fallback (no markers): the "--- Attempt …"-scoped tail
// with DEBUG: lines and any trailing CLI usage footer stripped, capped to the
// last 40 lines — matching the awk/grep pipeline the bash version used.
func ExtractSummary(logContents string) string {
	const maxLines = 40

	// Scope to the last attempt first so stale markers from an earlier attempt
	// in the same appended log can't win over the current one.
	last := lastAttempt(logContents)

	// Preferred: deterministic marker-delimited summary.
	if s, ok := betweenMarkers(last); ok {
		return s
	}

	// Filter out DEBUG: lines and any trailing CLI usage footer (e.g. Codex's
	// "tokens used" line).
	var kept []string
	for _, line := range strings.Split(stripUsageFooter(last), "\n") {
		if strings.HasPrefix(line, "DEBUG: ") {
			continue
		}
		kept = append(kept, line)
	}

	// Trim leading/trailing blank lines and cap to the last maxLines.
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

// lastAttempt returns logContents from after the final "--- Attempt …" marker
// to the end (the whole input if there is no marker).
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

// betweenMarkers returns the trimmed text between the last SummaryStartMarker
// and the first SummaryEndMarker after it. ok is false when the pair is absent,
// so the caller can fall back to the heuristic. Using the LAST start marker
// guards against the agent echoing the instruction earlier in its output.
func betweenMarkers(s string) (string, bool) {
	return between(s, SummaryStartMarker, SummaryEndMarker)
}

// between returns the trimmed text between the LAST start marker and the first
// end marker after it. Using the last start guards against the agent echoing
// the instruction earlier in its output. ok is false when either marker is
// absent or the span is empty.
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

// usageFooterRe matches a trailing token-usage footer some CLIs print after the
// agent's message (notably Codex's "tokens used: 167,195"). Anchored to the end
// so it never eats a legitimate mid-summary mention.
var usageFooterRe = regexp.MustCompile(`(?is)\n\s*tokens used\b.*$`)

// stripUsageFooter removes a trailing CLI usage footer if present.
func stripUsageFooter(s string) string {
	return usageFooterRe.ReplaceAllString(s, "")
}
