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

var (
	blockedRe   = regexp.MustCompile(`(?im)^BLOCKED:\s*(.*)$`)
	rateLimitRe = regexp.MustCompile(`(?i)rate.limit|usage.limit|exceeded.*limit|too many requests`)
)

// BlockedLine returns the first "BLOCKED: …" line in output (case-insensitive
// at the start of a line) or "" if none.
func BlockedLine(output string) string {
	m := blockedRe.FindString(output)
	return m
}

// HasRateLimit reports whether output contains any of the rate / usage limit
// markers Claude emits.
func HasRateLimit(output string) bool {
	return rateLimitRe.MatchString(output)
}

// ExtractSummary returns Claude's final attempt's summary, stripping the
// "--- Attempt …" markers and DEBUG: lines, and keeping only the last 40
// lines. Matches the awk/grep pipeline the bash version used to build the PR
// body.
func ExtractSummary(logContents string) string {
	const maxLines = 40

	// Find the start of the last attempt.
	idx := strings.LastIndex(logContents, "--- Attempt ")
	last := logContents
	if idx >= 0 {
		// Skip to the line after the marker.
		nl := strings.IndexByte(logContents[idx:], '\n')
		if nl >= 0 {
			last = logContents[idx+nl+1:]
		} else {
			last = ""
		}
	}

	// Filter out DEBUG: lines.
	var kept []string
	for _, line := range strings.Split(last, "\n") {
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
