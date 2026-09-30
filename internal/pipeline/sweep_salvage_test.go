package pipeline

import (
	"strings"
	"testing"

	"github.com/onelastcommit/noctra/internal/github"
	"github.com/onelastcommit/noctra/internal/sweep"
)

func salvageJob() sweep.Job {
	return sweep.Job{
		Task: sweep.Task{
			Name:         "dead-code",
			Description:  "Detect and remove unused code, imports, and variables",
			CommitPrefix: "refactor",
			PRLabel:      "maintenance",
		},
		RepoSlug:   "ahmadalmezaal-atomic-streaks",
		RepoPath:   "/repos/atomic-streaks",
		MainBranch: "main",
	}
}

func TestSalvagedPRTitleMarksThePartialRun(t *testing.T) {
	got := salvagedPRTitle("refactor", "Detect and remove unused code")
	if !strings.HasPrefix(got, "refactor: ") {
		t.Errorf("title = %q, want the conventional-commit prefix preserved", got)
	}
	if !strings.Contains(got, "(partial)") {
		t.Errorf("title = %q, want it flagged as partial", got)
	}
}

func TestSalvagedPRBodyCarriesTheNoctraMarker(t *testing.T) {
	body := salvagedPRBody(salvageJob(), "Hit the 5.0M token ceiling without finishing.", "3 files changed", "Claude Code")
	if !strings.Contains(body, github.NoctraPRBodyMarker) {
		t.Error("salvaged PR body lacks the Noctra marker, so auto-iterate would never claim it")
	}
}

func TestSalvagedPRBodyWarnsTheDiffIsUnverified(t *testing.T) {
	body := salvagedPRBody(salvageJob(), "Ran out of time after 20m0s without finishing.", "3 files changed", "Claude Code")

	for _, want := range []string{
		"UNVERIFIED",
		"Ran out of time after 20m0s without finishing.",
		"3 files changed",
		"dead-code",
		"ahmadalmezaal-atomic-streaks",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("salvaged PR body missing %q", want)
		}
	}
}

func TestTruncateDiffStatBoundsLongStats(t *testing.T) {
	short := " internal/a.go | 2 +-\n 1 file changed"
	if got := truncateDiffStat(short); got != strings.TrimSpace(short) {
		t.Errorf("truncateDiffStat(short) = %q, want it unchanged", got)
	}

	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(" internal/pkg/file.go | 3 +--\n")
	}
	got := truncateDiffStat(b.String())
	if lines := strings.Count(got, "\n") + 1; lines > 42 {
		t.Errorf("truncateDiffStat(long) kept %d lines, want it bounded", lines)
	}
	if !strings.Contains(got, "more changed file(s)") {
		t.Errorf("truncateDiffStat(long) = %q, want a truncation notice", got)
	}
}
