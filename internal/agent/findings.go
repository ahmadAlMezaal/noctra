package agent

import (
	"encoding/json"
	"strings"
)

// Finding markers wrap the per-finding status array the fix prompt asks for.
const (
	FindingsStartMarker = "===NOCTRA FINDINGS==="
	FindingsEndMarker   = "===END NOCTRA FINDINGS==="
)

// FindingReply is the agent's status for one numbered review finding: the
// 1-based Finding number from the fix prompt, whether code changed for it, and
// a one-sentence note for that finding's review thread.
type FindingReply struct {
	Finding   int    `json:"finding"`
	Addressed bool   `json:"addressed"`
	Reply     string `json:"reply"`
}

// ExtractFindingReplies parses the per-finding JSON array the agent wraps in the
// finding markers. Tolerant by design: absence, malformed JSON, or an empty
// result return ok=false so the caller falls back rather than guessing — not all
// backends emit the block reliably. Entries with a non-positive Finding or empty
// Reply are dropped; on a duplicate Finding the last entry wins.
func ExtractFindingReplies(logContents string) ([]FindingReply, bool) {
	raw, ok := between(lastAttempt(logContents), FindingsStartMarker, FindingsEndMarker)
	if !ok {
		return nil, false
	}

	var parsed []FindingReply
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, false
	}

	order := make([]int, 0, len(parsed))
	byFinding := make(map[int]FindingReply, len(parsed))
	for _, f := range parsed {
		f.Reply = strings.TrimSpace(f.Reply)
		if f.Finding < 1 || f.Reply == "" {
			continue
		}
		if _, seen := byFinding[f.Finding]; !seen {
			order = append(order, f.Finding)
		}
		byFinding[f.Finding] = f
	}
	if len(order) == 0 {
		return nil, false
	}

	out := make([]FindingReply, 0, len(order))
	for _, n := range order {
		out = append(out, byFinding[n])
	}
	return out, true
}
