package pipeline

import "testing"

func TestPRLabelsAPIPath(t *testing.T) {
	got, err := prLabelsAPIPath("https://github.com/ahmadAlMezaal/noctra/pull/241")
	if err != nil {
		t.Fatalf("prLabelsAPIPath: %v", err)
	}
	if want := "repos/ahmadAlMezaal/noctra/issues/241/labels"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := prLabelsAPIPath("https://github.com/owner/repo/issues/9"); err == nil {
		t.Error("a non-pull URL should error")
	}
}
