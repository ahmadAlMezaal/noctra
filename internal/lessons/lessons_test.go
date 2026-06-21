package lessons

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

func TestProcessMergedPRs(t *testing.T) {
	// Create mock binaries
	dir := t.TempDir()

	// Mock 'gh' script
	ghScript := `#!/bin/sh
case "$*" in
	*view*pull/1*)
		echo '{"url":"https://github.com/owner/repo/pull/1","number":1,"state":"MERGED","headRefOid":"def456"}'
		;;
	*view*pull/2*)
		echo '{"url":"https://github.com/owner/repo/pull/2","number":2,"state":"CLOSED","headRefOid":"xyz789"}'
		;;
	*view*pull/3*)
		echo '{"url":"https://github.com/owner/repo/pull/3","number":3,"state":"OPEN","headRefOid":"uvw012"}'
		;;
	*)
		exit 1
		;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(ghScript), 0o700); err != nil {
		t.Fatal(err)
	}

	// Mock 'git' script
	gitScript := `#!/bin/sh
case "$*" in
	*diff*)
		echo "dummy human edit diff content"
		;;
	*)
		exit 0
		;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(gitScript), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Create test state store
	dbPath := filepath.Join(dir, "state.db")
	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("failed to close store: %v", err)
		}
	}()

	// Setup tracked PRs
	const pr1 = "https://github.com/owner/repo/pull/1"
	const pr2 = "https://github.com/owner/repo/pull/2"
	const pr3 = "https://github.com/owner/repo/pull/3"

	// Merged, has last pushed SHA
	if err := store.Update(pr1, func(r *state.PRState) {
		r.TicketID = "ENG-1"
		r.LastPushedSHA = "abc123"
		r.MergedProcessed = false
	}); err != nil {
		t.Fatal(err)
	}

	// Closed, not merged
	if err := store.Update(pr2, func(r *state.PRState) {
		r.TicketID = "ENG-2"
		r.LastPushedSHA = "abc123"
		r.MergedProcessed = false
	}); err != nil {
		t.Fatal(err)
	}

	// Open
	if err := store.Update(pr3, func(r *state.PRState) {
		r.TicketID = "ENG-3"
		r.LastPushedSHA = "abc123"
		r.MergedProcessed = false
	}); err != nil {
		t.Fatal(err)
	}

	ghClient := github.New()
	resolver := &repo.Resolver{
		ReposBase: dir,
		RepoPath:  dir,
	}

	// Mock review gate using a dummy API key to make it "enabled"
	reviewGate := review.New("dummy_key", "gemini-2.5-pro")
	geminiScript := `#!/bin/sh
echo "Lesson 1: updated lessons from mock"
`
	if err := os.WriteFile(filepath.Join(dir, "gemini"), []byte(geminiScript), 0o700); err != nil {
		t.Fatal(err)
	}
	reviewGate.Mode = "cli"

	ProcessMergedPRs(context.Background(), store, ghClient, resolver, reviewGate)

	// Check if merged PR was processed and updated the lessons
	p1State := store.Get(pr1)
	if !p1State.MergedProcessed {
		t.Error("expected pr1 MergedProcessed to be true")
	}

	lessons, err := store.GetLessons("owner-repo")
	if err != nil {
		t.Fatal(err)
	}
	if lessons == "" {
		t.Error("expected lessons to be non-empty for owner-repo")
	}

	// Check if closed PR was marked processed (but no lessons update expected)
	p2State := store.Get(pr2)
	if !p2State.MergedProcessed {
		t.Error("expected pr2 MergedProcessed to be true")
	}

	// Check if open PR was skipped (MergedProcessed should still be false)
	p3State := store.Get(pr3)
	if p3State.MergedProcessed {
		t.Error("expected pr3 MergedProcessed to be false")
	}
}
