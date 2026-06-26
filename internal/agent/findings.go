package agent

import (
	"encoding/json"
	"strings"
)

// Finding markers wrap the per-finding status array in the agent's output.
const (
	FindingsStartMarker = "===NOCTRA FINDINGS==="
	FindingsEndMarker   = "===END NOCTRA FINDINGS==="
)

// FindingReply is the agent's status for one numbered review finding.
type FindingReply struct {
	Finding   int    `json:"finding"`
	Addressed bool   `json:"addressed"`
	Reply     string `json:"reply"`
}

// ExtractFindingReplies parses the per-finding JSON array from the finding
// markers; ok is false when it's absent, malformed, or empty.
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
