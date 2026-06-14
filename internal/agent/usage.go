package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// Usage holds token and cost information parsed from an agent CLI's output.
// All fields are best-effort: backends that don't print usage stats yield zeros.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	CostUSD      float64
}

// Usage-parsing regexes — intentionally generous to cover the known CLI output
// formats (Codex "tokens used: 167,195", Claude/Copilot session cost, etc.)
// and any future format that follows common phrasing.
var (
	tokensUsedRe   = regexp.MustCompile(`(?i)tokens?\s+used[:\s]+([0-9,]+)`)
	totalTokensRe  = regexp.MustCompile(`(?i)total\s+tokens?[:\s]+([0-9,]+)`)
	inputTokensRe  = regexp.MustCompile(`(?i)input\s+tokens?[:\s]+([0-9,]+)`)
	outputTokensRe = regexp.MustCompile(`(?i)output\s+tokens?[:\s]+([0-9,]+)`)
	costRe         = regexp.MustCompile(`(?i)(?:total\s+)?cost[:\s]+\$([0-9,.]+)`)
)

// ParseUsage extracts token and cost information from agent CLI output.
// Returns zero-valued fields for anything not found — callers should treat
// zeros as "not reported" rather than "no usage".
func ParseUsage(output string) Usage {
	var u Usage

	if m := inputTokensRe.FindStringSubmatch(output); len(m) > 1 {
		u.InputTokens = parseCommaInt(m[1])
	}
	if m := outputTokensRe.FindStringSubmatch(output); len(m) > 1 {
		u.OutputTokens = parseCommaInt(m[1])
	}

	// Total tokens: prefer an explicit "total tokens" line, fall back to the
	// more common "tokens used" phrasing (Codex).
	if m := totalTokensRe.FindStringSubmatch(output); len(m) > 1 {
		u.TotalTokens = parseCommaInt(m[1])
	} else if m := tokensUsedRe.FindStringSubmatch(output); len(m) > 1 {
		u.TotalTokens = parseCommaInt(m[1])
	}

	// If we got input/output but no explicit total, sum them.
	if u.TotalTokens == 0 && (u.InputTokens > 0 || u.OutputTokens > 0) {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}

	if m := costRe.FindStringSubmatch(output); len(m) > 1 {
		u.CostUSD, _ = strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	}

	return u
}

// parseCommaInt parses a number string that may contain commas (e.g. "167,195").
func parseCommaInt(s string) int64 {
	n, _ := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
	return n
}
