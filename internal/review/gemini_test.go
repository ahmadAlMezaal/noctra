package review

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewCLIParsesPass(t *testing.T) {
	dir := t.TempDir()
	writeFakeGemini(t, dir, `#!/bin/sh
case "$*" in
  *"--model gemini-test"*) ;;
  *) echo "missing model" >&2; exit 2 ;;
esac
case "$*" in
  *"--prompt"*) echo "prompt should be sent on stdin" >&2; exit 2 ;;
esac
found=
while IFS= read -r line; do
  case "$line" in
    *"DIFF_CONTENT"*) found=1 ;;
  esac
done
if [ "$found" != "1" ]; then
  echo "missing stdin prompt" >&2
  exit 2
fi
echo "VERDICT: PASS"
echo "Looks good."
`)
	t.Setenv("PATH", dir)

	g := NewWithMode("cli", "", "gemini-test")
	got, err := g.Review(context.Background(), "Ticket", "Description", "DIFF_CONTENT")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !got.Passed || got.Skipped {
		t.Fatalf("result = %+v, want passed and not skipped", got)
	}
	if !strings.Contains(got.Body, "Looks good.") {
		t.Errorf("Body = %q", got.Body)
	}
}

func TestReviewCLIMissingIsUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	g := NewWithMode("cli", "", "gemini-test")
	got, err := g.Review(context.Background(), "Ticket", "Description", "diff")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !got.Skipped || !got.Passed {
		t.Fatalf("result = %+v, want skipped pass", got)
	}
	if !strings.Contains(got.Body, "not found") {
		t.Errorf("Body = %q, want missing CLI hint", got.Body)
	}
}

func TestReviewCLIAuthFailureIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeFakeGemini(t, dir, `#!/bin/sh
echo "not logged in: run gemini first" >&2
exit 1
`)
	t.Setenv("PATH", dir)

	g := NewWithMode("cli", "", "gemini-test")
	got, err := g.Review(context.Background(), "Ticket", "Description", "diff")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !got.Skipped || !strings.Contains(got.Body, "not logged in") {
		t.Fatalf("result = %+v, want skipped auth hint", got)
	}
}

func TestReviewCLINonAuthFailureIsNotUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeFakeGemini(t, dir, `#!/bin/sh
echo "rate limit exceeded" >&2
exit 1
`)
	t.Setenv("PATH", dir)

	g := NewWithMode("cli", "", "gemini-test")
	got, err := g.Review(context.Background(), "Ticket", "Description", "diff")
	if err == nil {
		t.Fatal("Review returned nil error, want CLI failure")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, should not be ErrUnavailable", err)
	}
	if got.Skipped || got.Passed {
		t.Fatalf("result = %+v, want non-skipped failure", got)
	}
}

func TestReviewAPIRequiresKeyForEnabled(t *testing.T) {
	if NewWithMode("api", "", "").Enabled() {
		t.Fatal("api mode without GEMINI_API_KEY should be disabled")
	}
	if !NewWithMode("cli", "", "").Enabled() {
		t.Fatal("cli mode should be enabled without GEMINI_API_KEY")
	}
}

func TestReviewAPISendsKeyInHeaderNotQuery(t *testing.T) {
	var gotQuery, gotKey string
	g := NewWithMode("api", "secret-key", "gemini-test")
	g.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotQuery = req.URL.RawQuery
		gotKey = req.Header.Get("x-goog-api-key")
		return jsonResponse(200, `{"candidates":[{"content":{"parts":[{"text":"VERDICT: PASS\nok"}]}}]}`), nil
	})}

	res, err := g.Review(context.Background(), "Ticket", "Description", "diff")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !res.Passed || res.Skipped {
		t.Fatalf("result = %+v, want passed", res)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q, want no API key query params", gotQuery)
	}
	if gotKey != "secret-key" {
		t.Fatalf("x-goog-api-key = %q, want secret-key", gotKey)
	}
}

func TestReviewAPINon2xxIsUnavailable(t *testing.T) {
	g := NewWithMode("api", "secret-key", "gemini-test")
	g.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(429, `{"error":{"message":"quota exceeded"}}`), nil
	})}

	res, err := g.Review(context.Background(), "Ticket", "Description", "diff")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !res.Skipped || !res.Passed {
		t.Fatalf("result = %+v, want skipped pass", res)
	}
}

func TestReviewAPIErrorBodyIsConcise(t *testing.T) {
	raw := `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan.\n* Quota exceeded for metric: foo\n* Quota exceeded for metric: bar","status":"RESOURCE_EXHAUSTED"}}`
	g := NewWithMode("api", "secret-key", "gemini-test")
	g.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(429, raw), nil
	})}

	res, _ := g.Review(context.Background(), "Ticket", "Description", "diff")
	if strings.Contains(res.Body, "Quota exceeded for metric") {
		t.Errorf("body should not include the raw multi-line blob: %q", res.Body)
	}
	if !strings.Contains(res.Body, "You exceeded your current quota") {
		t.Errorf("body should include the first line of error.message: %q", res.Body)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	long := strings.Repeat("x", 500)
	cases := []struct {
		name, raw, want string
	}{
		{"structured first line", `{"error":{"message":"boom\ndetail line"}}`, "boom"},
		{"non-json falls back", "plain text error", "plain text error"},
		{"empty falls back to status", "", "503 Service Unavailable"},
	}
	for _, c := range cases {
		if got := apiErrorMessage([]byte(c.raw), "503 Service Unavailable"); got != c.want {
			t.Errorf("%s: apiErrorMessage = %q, want %q", c.name, got, c.want)
		}
	}
	if got := apiErrorMessage([]byte(long), "503"); len(got) > 320 || !strings.HasSuffix(got, "…") {
		t.Errorf("long raw body should be capped with ellipsis, got len %d", len(got))
	}
}

func TestReviewAPINoCandidatesIsUnavailable(t *testing.T) {
	g := NewWithMode("api", "secret-key", "gemini-test")
	g.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{}`), nil
	})}

	res, err := g.Review(context.Background(), "Ticket", "Description", "diff")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !res.Skipped || !res.Passed {
		t.Fatalf("result = %+v, want skipped pass", res)
	}
}

func TestReviewAPISafetyBlockIsUnavailable(t *testing.T) {
	g := NewWithMode("api", "secret-key", "gemini-test")
	g.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"promptFeedback":{"blockReason":"SAFETY"}}`), nil
	})}

	res, err := g.Review(context.Background(), "Ticket", "Description", "diff")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !res.Skipped || !res.Passed || !strings.Contains(res.Body, "SAFETY") {
		t.Fatalf("result = %+v, want skipped safety body", res)
	}
}

func TestParseResultToleratesVerdictFormatting(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		passed  bool
		skipped bool
	}{
		{name: "bolded", text: "**VERDICT: PASS**\nLooks good.", passed: true},
		{name: "bolded label", text: "**VERDICT**: PASS\nLooks good.", passed: true},
		{name: "missing space", text: "VERDICT:PASS\nLooks good.", passed: true},
		{name: "fail", text: "**VERDICT: FAIL**\nNeeds work.", passed: false},
		{name: "bolded fail label", text: "**VERDICT**: FAIL\nNeeds work.", passed: false},
		{name: "unparseable", text: "Looks fine to me.", passed: true, skipped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseResult(tt.text)
			if got.Passed != tt.passed || got.Skipped != tt.skipped {
				t.Fatalf("parseResult(%q) = %+v, want passed=%v skipped=%v",
					tt.text, got, tt.passed, tt.skipped)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func writeFakeGemini(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "gemini")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
