package agent

import (
	"strings"
	"testing"
)

func TestBuildFixPrompt_IncludesTicketContextAndAllFeedback(t *testing.T) {
	out := BuildFixPrompt(FixPromptInput{
		Identifier:  "ENG-42",
		Title:       "Add custom font family",
		Description: "We want Inter across the app.",
		PRNumber:    59,
		PRURL:       "https://github.com/me/trade-mate/pull/59",
		Feedback: []FeedbackItem{
			{
				Kind:   "comment",
				Author: "alice",
				Body:   "Don't forget the bold variant",
				URL:    "https://github.com/me/trade-mate/pull/59#issuecomment-1",
			},
			{
				Kind:   "review",
				State:  "CHANGES_REQUESTED",
				Author: "bob",
				Body:   "The fallback chain is missing.",
			},
		},
	})

	for _, want := range []string{
		"ENG-42",
		"Add custom font family",
		"We want Inter across the app.",
		"#59",
		"https://github.com/me/trade-mate/pull/59",
		"@alice",
		"@bob",
		"Don't forget the bold variant",
		"The fallback chain is missing.",
		"Review (CHANGES_REQUESTED)",
		"BLOCKED:", // the rules section
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fix prompt missing %q\n---\n%s", want, out)
		}
	}
}

func TestBuildFixPrompt_RendersInlineCommentLocation(t *testing.T) {
	out := BuildFixPrompt(FixPromptInput{
		Identifier: "ENG-42",
		Title:      "Fix race",
		Feedback: []FeedbackItem{{
			Kind:   "comment",
			Author: "alice",
			Body:   "keep the mutex held",
			Path:   "internal/state/state.go",
			Line:   122,
		}},
	})
	if !strings.Contains(out, "internal/state/state.go:122") {
		t.Errorf("expected inline comment location in prompt\n---\n%s", out)
	}
}

func TestBuildFixPrompt_RendersCIFailures(t *testing.T) {
	out := BuildFixPrompt(FixPromptInput{
		Identifier: "ENG-50",
		Title:      "Thing",
		PRNumber:   7,
		PRURL:      "https://github.com/me/repo/pull/7",
		CI: []CIItem{{
			Name: "test",
			URL:  "https://github.com/me/repo/actions/runs/1",
			Logs: "FAIL: TestThing\nexpected 2 got 3",
		}},
	})
	for _, want := range []string{
		"Failing CI checks to fix",
		"test",
		"expected 2 got 3",
		"reproduce it locally",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CI prompt missing %q\n---\n%s", want, out)
		}
	}
}

func TestBuildFixPrompt_HandlesMissingDescription(t *testing.T) {
	out := BuildFixPrompt(FixPromptInput{
		Identifier: "ENG-1",
		Title:      "Tiny fix",
		Feedback:   []FeedbackItem{{Kind: "comment", Author: "alice", Body: "do it"}},
	})
	if !strings.Contains(out, "No description provided.") {
		t.Error("expected fallback description placeholder")
	}
}

func TestSectionLabel(t *testing.T) {
	cases := map[[2]string]string{
		{"review", "CHANGES_REQUESTED"}: "Review (CHANGES_REQUESTED)",
		{"review", ""}:                  "Review",
		{"comment", ""}:                 "Comment",
		{"comment", "anything"}:         "Comment",
	}
	for in, want := range cases {
		if got := sectionLabel(in[0], in[1]); got != want {
			t.Errorf("sectionLabel(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestBuildFixPrompt_IncludesLessons(t *testing.T) {
	out := BuildFixPrompt(FixPromptInput{
		Identifier:  "ENG-42",
		Title:       "Title",
		PRNumber:    59,
		PRURL:       "url",
		RepoLessons: "- Lesson A\n- Lesson B",
	})
	if !strings.Contains(out, "## Repository Lessons & Conventions") {
		t.Errorf("expected lessons heading in fix prompt:\n%s", out)
	}
	if !strings.Contains(out, "- Lesson A") {
		t.Errorf("expected lesson content in fix prompt:\n%s", out)
	}
}
