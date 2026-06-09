package review

import (
	"context"
	"errors"
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

func writeFakeGemini(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "gemini")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
