package linear

import (
	"testing"

	"github.com/onelastcommit/noctra/internal/github"
)

func TestRepoDirectiveSurvivesLinearAutoLinkification(t *testing.T) {
	content := "Project notes.\n\n---\n\nRepo: [https://github.com/acme/app](<https://github.com/acme/app>)"

	p := &Project{Content: content}
	ref, _ := p.RepoDirective()
	if ref == "" {
		t.Fatal("RepoDirective found no ref")
	}

	got, err := github.ExtractOwnerRepo(ref)
	if err != nil {
		t.Fatalf("ExtractOwnerRepo(%q): %v", ref, err)
	}
	if got != "acme/app" {
		t.Errorf("ExtractOwnerRepo(%q) = %q, want %q", ref, got, "acme/app")
	}
}
