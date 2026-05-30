// Package review is the optional Gemini-based "second model review" gate.
// Nightshift sends the staged diff and ticket context to Gemini and parses a
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
	"strings"
	"time"
)

// Result is the outcome of a single review pass.
type Result struct {
	Passed bool
	// Body is Gemini's review text — surfaced in the PR body if the gate
	// did not pass.
	Body string
}

// Gate is a Gemini-backed reviewer.
type Gate struct {
	APIKey string
	Model  string
	HTTP   *http.Client
}

// New returns a Gate. model defaults to "gemini-2.5-pro" when empty.
func New(apiKey, model string) *Gate {
	if model == "" {
		model = "gemini-2.5-pro"
	}
	return &Gate{
		APIKey: apiKey,
		Model:  model,
		HTTP:   &http.Client{Timeout: 120 * time.Second},
	}
}

// Enabled reports whether the gate is configured to run.
func (g *Gate) Enabled() bool { return g != nil && g.APIKey != "" }

// Review sends a diff + ticket context to Gemini and returns its verdict.
func (g *Gate) Review(ctx context.Context, ticketTitle, ticketDescription, diff string) (Result, error) {
	if !g.Enabled() {
		return Result{Passed: true, Body: "PASS (Gemini not configured)"}, nil
	}

	prompt := fmt.Sprintf(`You are a senior code reviewer. Review this diff against the ticket requirements.

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

	body, err := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"parts": []any{map[string]any{"text": prompt}}}},
		"generationConfig": map[string]any{"temperature": 0.1, "maxOutputTokens": 4096},
	})
	if err != nil {
		return Result{}, err
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		g.Model, g.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}

	var parsed struct {
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
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return Result{}, errors.New("gemini response had no candidates")
	}

	text := parsed.Candidates[0].Content.Parts[0].Text
	return Result{Passed: strings.Contains(strings.ToUpper(text), "VERDICT: PASS"), Body: text}, nil
}
