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
echo "VERDICT: PASS"
echo "Looks good."
`)
	t.Setenv("PATH", dir)

	g := NewWithMode("cli", "", "gemini-test")
	got, err := g.Review(context.Background(), "Ticket", "Description", "diff")
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

func TestReviewCLINonZeroIsUnavailable(t *testing.T) {
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
