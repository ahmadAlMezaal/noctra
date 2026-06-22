// Package review is the optional Gemini-based "second model review" gate.
// Noctra sends the staged diff and ticket context to Gemini and parses a
// VERDICT: PASS / VERDICT: FAIL line out of the response.
package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const defaultMode = "api"

// ErrUnavailable means the selected Gemini backend is not usable on this host
// (for example: CLI missing or not logged in). Callers should skip the optional
// review gate instead of treating this like a failed review.
var ErrUnavailable = errors.New("gemini review unavailable")

// Finding is one line-anchored review comment.
type Finding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Comment  string `json:"comment"`
}

// Result is the outcome of a single review pass.
type Result struct {
	Passed bool
	// Skipped reports that the optional gate could not run because its selected
	// backend is not available on this host.
	Skipped bool
	// Body is Gemini's review text — surfaced in the PR body if the gate
	// did not pass.
	Body string
	// Summary is the short overall assessment (structured API mode only).
	Summary string
	// Findings are line-anchored comments to post inline (structured API mode only).
	Findings []Finding
}

// Render flattens a structured result into readable text for the fix prompt and
// the PR-body fallback.
func (r Result) Render() string {
	var b strings.Builder
	if s := strings.TrimSpace(r.Summary); s != "" {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, f := range r.Findings {
		loc := f.Path
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		sev := ""
		if f.Severity != "" {
			sev = strings.ToUpper(f.Severity) + " "
		}
		fmt.Fprintf(&b, "- [%s%s] %s\n", sev, loc, strings.TrimSpace(f.Comment))
	}
	return strings.TrimSpace(b.String())
}

// Gate is a Gemini-backed reviewer.
type Gate struct {
	Mode   string
	APIKey string
	Model  string
	HTTP   *http.Client
}

// New returns a Gate. model defaults to "gemini-2.5-pro" when empty.
func New(apiKey, model string) *Gate {
	return NewWithMode(defaultMode, apiKey, model)
}

// NewWithMode returns a Gate for either the Gemini API or Gemini CLI.
func NewWithMode(mode, apiKey, model string) *Gate {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = defaultMode
	}
	if model == "" {
		model = "gemini-2.5-pro"
	}
	return &Gate{
		Mode:   mode,
		APIKey: apiKey,
		Model:  model,
		HTTP:   &http.Client{Timeout: 120 * time.Second},
	}
}

// Enabled reports whether the gate is configured to run.
func (g *Gate) Enabled() bool {
	if g == nil {
		return false
	}
	return g.Mode == "cli" || g.APIKey != ""
}

// Review sends a diff + ticket context to Gemini and returns its verdict.
func (g *Gate) Review(ctx context.Context, ticketTitle, ticketDescription, diff string) (Result, error) {
	if !g.Enabled() {
		return Result{Passed: true, Body: "PASS (Gemini not configured)"}, nil
	}

	if g.Mode == "cli" {
		return g.reviewCLI(ctx, buildPrompt(ticketTitle, ticketDescription, diff))
	}
	return g.reviewAPI(ctx, buildStructuredPrompt(ticketTitle, ticketDescription, diff), true)
}

func buildPrompt(ticketTitle, ticketDescription, diff string) string {
	return fmt.Sprintf(`You are a senior code reviewer. Review this diff against the ticket requirements.

## Ticket: %s
%s

## Diff:
%s

## Review for:
1. Does the diff fully implement the ticket requirements?
2. Are there any bugs, logic errors, or edge cases missed?
3. Are there security concerns?
4. Does it follow reasonable coding conventions?
5. Are tests included and do they cover the key scenarios?

## Response format:
Start your response with exactly one of:
- VERDICT: PASS — if the implementation is good to merge (minor nits are fine)
- VERDICT: FAIL — if there are issues that should be fixed before merging

Then provide your review comments.`,
		ticketTitle, ticketDescription, diff)
}

func buildStructuredPrompt(ticketTitle, ticketDescription, diff string) string {
	return fmt.Sprintf(`You are a senior code reviewer. Review this diff against the ticket requirements.

## Ticket: %s
%s

## Diff:
%s

Return a JSON review:
- "verdict": "PASS" if it is good to merge (minor nits are fine), otherwise "FAIL".
- "summary": a 1-3 sentence overall assessment.
- "findings": specific issues, each anchored to a changed line. For each finding set "path" (the file path exactly as it appears in the diff), "line" (a line number that appears as an added/changed line in the diff), "severity" ("high"/"medium"/"low"), and "comment" (the problem and a suggested fix; wrap any code, suggested replacements, or HTML in a fenced Markdown code block — a triple-backtick fence — rather than inline code spans, so it renders correctly on GitHub instead of as raw markup). Only include findings you can anchor to a changed line; put anything broader in the summary. Use an empty array if there are none.`,
		ticketTitle, ticketDescription, diff)
}

func reviewSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"PASS", "FAIL"}},
			"summary": map[string]any{"type": "string"},
			"findings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":     map[string]any{"type": "string"},
						"line":     map[string]any{"type": "integer"},
						"severity": map[string]any{"type": "string"},
						"comment":  map[string]any{"type": "string"},
					},
					"required": []string{"path", "comment"},
				},
			},
		},
		"required": []string{"verdict", "summary"},
	}
}

func (g *Gate) reviewAPI(ctx context.Context, prompt string, structured bool) (Result, error) {
	gen := map[string]any{"temperature": 0.1, "maxOutputTokens": 4096}
	if structured {
		gen["responseMimeType"] = "application/json"
		gen["responseSchema"] = reviewSchema()
	}
	body, err := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"parts": []any{map[string]any{"text": prompt}}}},
		"generationConfig": gen,
	})
	if err != nil {
		return Result{}, err
	}

	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent",
		url.PathEscape(g.Model))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return Result{Skipped: true, Passed: true, Body: fmt.Sprintf("Gemini API request failed: %v", err)},
			fmt.Errorf("%w: gemini request: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{Skipped: true, Passed: true, Body: fmt.Sprintf("Gemini API unavailable (%s): %s", resp.Status, apiErrorMessage(raw, resp.Status))},
			fmt.Errorf("%w: gemini API returned %s", ErrUnavailable, resp.Status)
	}

	var parsed struct {
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{}, fmt.Errorf("decode gemini response: %w", err)
	}
	if parsed.PromptFeedback.BlockReason != "" {
		body := "Gemini API safety-blocked the review prompt: " + parsed.PromptFeedback.BlockReason
		return Result{Skipped: true, Passed: true, Body: body},
			fmt.Errorf("%w: %s", ErrUnavailable, body)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return Result{Skipped: true, Passed: true, Body: "Gemini API returned no review candidates."},
			fmt.Errorf("%w: gemini response had no candidates", ErrUnavailable)
	}

	text := parsed.Candidates[0].Content.Parts[0].Text
	if structured {
		if r, ok := parseStructured(text); ok {
			return r, nil
		}
	}
	return parseResult(text), nil
}

func parseStructured(text string) (Result, bool) {
	var s struct {
		Verdict  string    `json:"verdict"`
		Summary  string    `json:"summary"`
		Findings []Finding `json:"findings"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &s) != nil {
		return Result{}, false
	}
	switch strings.ToUpper(strings.TrimSpace(s.Verdict)) {
	case "PASS":
		r := Result{Passed: true, Summary: strings.TrimSpace(s.Summary), Findings: s.Findings}
		r.Body = r.Render()
		return r, true
	case "FAIL":
		r := Result{Passed: false, Summary: strings.TrimSpace(s.Summary), Findings: s.Findings}
		r.Body = r.Render()
		return r, true
	}
	return Result{}, false
}

func apiErrorMessage(raw []byte, status string) string {
	const maxLen = 300
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if msg := strings.TrimSpace(parsed.Error.Message); msg != "" {
			if i := strings.IndexByte(msg, '\n'); i > 0 {
				msg = strings.TrimSpace(msg[:i])
			}
			return truncateRunes(msg, maxLen)
		}
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return status
	}
	return truncateRunes(body, maxLen)
}

func truncateRunes(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "…"
}

func (g *Gate) reviewCLI(ctx context.Context, prompt string) (Result, error) {
	if _, err := exec.LookPath("gemini"); err != nil {
		return Result{Skipped: true, Passed: true, Body: "Gemini CLI not found in PATH. Install it and run `gemini` once to log in."},
			fmt.Errorf("%w: gemini CLI not found in PATH", ErrUnavailable)
	}

	cmd := exec.CommandContext(ctx, "gemini", "--model", g.Model)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		msg := text
		if msg == "" {
			msg = err.Error()
		}
		if isCLIUnavailableMessage(msg) {
			return Result{Skipped: true, Passed: true, Body: fmt.Sprintf("Gemini CLI unavailable: %s", msg)},
				fmt.Errorf("%w: gemini CLI failed: %s", ErrUnavailable, msg)
		}
		return Result{}, fmt.Errorf("gemini CLI failed: %s", msg)
	}
	if text == "" {
		return Result{}, errors.New("gemini CLI returned no output")
	}
	return parseResult(text), nil
}

func isCLIUnavailableMessage(msg string) bool {
	msg = strings.ToLower(msg)
	authHints := []string{
		"not logged in",
		"login required",
		"not authenticated",
		"authentication required",
		"auth required",
		"no credentials",
		"could not load credentials",
		"run gemini first",
		"run gemini once",
	}
	for _, hint := range authHints {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func parseResult(text string) Result {
	verdict := reviewVerdict(text)
	return Result{
		Passed:  verdict == "PASS" || verdict == "",
		Skipped: verdict == "",
		Body:    text,
	}
}

var verdictLineRe = regexp.MustCompile(`(?i)(?:\*\*)?\s*VERDICT\s*:?\s*(PASS|FAIL)\b`)

func reviewVerdict(text string) string {
	for _, line := range strings.Split(text, "\n") {
		normalized := strings.ReplaceAll(line, "*", "")
		m := verdictLineRe.FindStringSubmatch(normalized)
		if len(m) == 2 {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}

// SummarizeLessons uses Gemini to consolidate a new post-merge diff with existing lessons.
func (g *Gate) SummarizeLessons(ctx context.Context, existingLessons, diff string) (string, error) {
	if !g.Enabled() {
		return "", errors.New("gemini not enabled")
	}

	const capBytes = 60000
	if len(diff) > capBytes {
		diff = diff[:capBytes] + "\n\n[Diff truncated...]"
	}

	prompt := fmt.Sprintf(`You are updating a compact, durable list of lessons and conventions for a repository based on human post-merge edits to AI-generated code.

Existing lessons/conventions for this repository:
%s

New human edits (diff of changes made by human on top of the AI's version):
%s

Incorporate any new correction patterns, missing conventions, or style guidelines from the new edits into the existing list.
Follow these rules:
1. Keep the final list extremely concise and action-oriented.
2. Keep the final list under 10 bullet points / 300 words total (size-bounded).
3. Directly focus on what the AI got wrong and how to avoid/fix it in the future.
4. Output ONLY the updated, consolidated list of lessons/conventions. Do not include any conversational filler, markdown headers like "Here is the updated list", or wrappers.

Updated lessons list:`,
		existingLessons, diff)

	var result Result
	var err error
	if g.Mode == "cli" {
		result, err = g.reviewCLI(ctx, prompt)
	} else {
		result, err = g.reviewAPI(ctx, prompt, false)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Body), nil
}
