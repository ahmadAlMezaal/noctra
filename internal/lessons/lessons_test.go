package lessons

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ahmadAlMezaal/noctra/internal/github"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/review"
	"github.com/ahmadAlMezaal/noctra/internal/state"
)

func TestProcessMergedPRs(t *testing.T) {
	dir := t.TempDir()

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

	gitScript := `#!/bin/sh
case "$*" in
	*log*)
		printf 'aaa111\037Ahmad\037docs: tighten wording\n\036'
		;;
	*show*)
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

	const pr1 = "https://github.com/owner/repo/pull/1"
	const pr2 = "https://github.com/owner/repo/pull/2"
	const pr3 = "https://github.com/owner/repo/pull/3"

	if err := store.Update(pr1, func(r *state.PRState) {
		r.TicketID = "ENG-1"
		r.LastPushedSHA = "abc123"
		r.MergedProcessed = false
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.Update(pr2, func(r *state.PRState) {
		r.TicketID = "ENG-2"
		r.LastPushedSHA = "abc123"
		r.MergedProcessed = false
	}); err != nil {
		t.Fatal(err)
	}

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

	reviewGate := review.New("dummy_key", "gemini-2.5-pro")
	geminiScript := `#!/bin/sh
echo "Lesson 1: updated lessons from mock"
`
	if err := os.WriteFile(filepath.Join(dir, "gemini"), []byte(geminiScript), 0o700); err != nil {
		t.Fatal(err)
	}
	reviewGate.Mode = "cli"

	ProcessMergedPRs(context.Background(), store, ghClient, resolver, reviewGate)

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

	p2State := store.Get(pr2)
	if !p2State.MergedProcessed {
		t.Error("expected pr2 MergedProcessed to be true")
	}

	p3State := store.Get(pr3)
	if p3State.MergedProcessed {
		t.Error("expected pr3 MergedProcessed to be false")
	}
}

func TestHumanCommitsSkipsNoctraAndBots(t *testing.T) {
	record := func(sha, author, body string) string {
		return sha + commitFieldSep + author + commitFieldSep + body + commitRecordSep
	}
	gitLog := record("n1", "Ahmad", "feat: implement ENG-1\n\nImplemented by Noctra using Claude Code\n") +
		"\n" + record("n2", "Ahmad", "fix: address PR feedback on ENG-1\n\nFollow-up commit by Noctra (1 review).\n") +
		"\n" + record("n3", "Ahmad", "chore: lint\n\nAutonomous maintenance by Noctra using Claude Code\n") +
		"\n" + record("b1", "dependabot[bot]", "chore(deps): bump x\n") +
		"\n" + record("h1", "Ahmad", "fix: use semicolons\n") +
		"\n" + record("h2", "Ahmad", "docs: mention Noctra in the README\n")

	got := humanCommits(gitLog)
	want := []string{"h1", "h2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("humanCommits = %v, want %v", got, want)
	}
}

func TestHumanEditsDiffIgnoresNoctraBotAndMergedInCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(env []string, file, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(msg), 0o600); err != nil {
			t.Fatal(err)
		}
		git(nil, "add", "-A")
		git(env, "commit", "-q", "-m", msg)
	}
	bot := []string{"GIT_AUTHOR_NAME=renovate[bot]", "GIT_AUTHOR_EMAIL=bot@example.com"}

	git(nil, "init", "-q", "-b", "main")
	git(nil, "config", "user.email", "t@t")
	git(nil, "config", "user.name", "T")
	git(nil, "config", "commit.gpgsign", "false")
	commit(nil, "base.txt", "base")
	git(nil, "checkout", "-q", "-b", "pr")
	commit(nil, "noctra.txt", "feat: work\n\nImplemented by Noctra using Claude Code")
	base := git(nil, "rev-parse", "HEAD")

	git(nil, "checkout", "-q", "main")
	commit(nil, "feature-on-main.txt", "feat: unrelated feature on main")
	git(nil, "checkout", "-q", "pr")
	git(nil, "merge", "-q", "--no-edit", "--no-ff", "main")
	commit(bot, "bot.txt", "chore: bot tweak")
	commit(nil, "late-noctra.txt", "fix: address PR feedback\n\nFollow-up commit by Noctra (1 review).")
	commit(nil, "human.txt", "fix: human correction")

	diff, err := humanEditsDiff(context.Background(), dir, base, "HEAD")
	if err != nil {
		t.Fatalf("humanEditsDiff: %v", err)
	}
	if !strings.Contains(diff, "human.txt") {
		t.Errorf("diff is missing the human commit:\n%s", diff)
	}
	for _, unwanted := range []string{"feature-on-main.txt", "bot.txt", "late-noctra.txt", "noctra.txt\n"} {
		if strings.Contains(diff, unwanted) {
			t.Errorf("diff should not contain %q:\n%s", unwanted, diff)
		}
	}
}
