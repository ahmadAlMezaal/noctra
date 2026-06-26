package github

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTailString_KeepsValidUTF8(t *testing.T) {
	// A run of 3-byte runes (★ = e2 98 85) so a raw byte cut would land
	// mid-rune for most max values.
	s := strings.Repeat("★", 100)
	for _, max := range []int{10, 50, 101, 299} {
		out := tailString(s, max)
		if !utf8.ValidString(out) {
			t.Errorf("tailString(max=%d) produced invalid UTF-8: %q", max, out)
		}
	}
	// Short input is returned untouched.
	if got := tailString("hi", 100); got != "hi" {
		t.Errorf("short input: got %q", got)
	}
}

func TestCheckLogs_NonActionsReturnsSentinel(t *testing.T) {
	_, err := New().CheckLogs(context.Background(), Check{DetailsURL: "https://circleci.com/gh/me/repo/123"})
	if !errors.Is(err, ErrNotActionsRun) {
		t.Errorf("expected ErrNotActionsRun, got %v", err)
	}
}

func TestListNoctraPRsRequiresBodyMarker(t *testing.T) {
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte(`#!/bin/sh
cat <<'JSON'
[
  {"url":"https://github.com/me/repo/pull/1","number":1,"title":"owned","headRefName":"noctra/eng-1","body":"*Implemented by [Noctra](https://github.com/ahmadAlMezaal/noctra) using Claude Code*"},
  {"url":"https://github.com/me/repo/pull/2","number":2,"title":"manual","headRefName":"noctra/eng-2","body":"manual PR"},
  {"url":"https://github.com/me/repo/pull/3","number":3,"title":"other","headRefName":"feat/eng-3","body":"*Implemented by [Noctra](https://github.com/ahmadAlMezaal/noctra)*"},
  {"url":"https://github.com/me/repo/pull/4","number":4,"title":"sweep-legacy","headRefName":"noctra/sweep-deps-update","body":"*Autonomous maintenance by [Noctra](https://github.com/ahmadAlMezaal/noctra) using Claude Code*"},
  {"url":"https://github.com/me/repo/pull/5","number":5,"title":"hidden-marker","headRefName":"noctra/sweep-lint-cleanup","body":"## cleanup\n<!-- noctra-authored -->"}
]
JSON
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := New().ListNoctraPRs(context.Background(), []string{"me/repo"})
	if err != nil {
		t.Fatalf("ListNoctraPRs: %v", err)
	}
	// Expect: #1 (ticket, legacy footer), #4 (sweep, legacy footer), #5 (hidden
	// marker only). Skipped: #2 (no marker), #3 (non-noctra branch).
	var nums []int
	for _, pr := range got {
		nums = append(nums, pr.Number)
	}
	want := []int{1, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("got PRs %v, want %v", nums, want)
	}
	for i, pr := range got {
		if pr.Number != want[i] {
			t.Fatalf("PR[%d] = #%d, want #%d (all: %v)", i, pr.Number, want[i], nums)
		}
		if pr.RepoURL != "me/repo" {
			t.Fatalf("PR #%d missing RepoURL: %+v", pr.Number, pr)
		}
	}
}

func TestIsNoctraAuthoredBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"hidden marker", "anything\n" + NoctraPRBodyMarker, true},
		{"ticket legacy footer", "*Implemented by [Noctra](https://x) using Claude Code*", true},
		{"sweep legacy footer", "*Autonomous maintenance by [Noctra](https://x)*", true},
		{"manual PR", "just a manual PR body", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := IsNoctraAuthoredBody(c.body); got != c.want {
			t.Errorf("%s: IsNoctraAuthoredBody(%q) = %v, want %v", c.name, c.body, got, c.want)
		}
	}
}

func TestDecodeReviewComments(t *testing.T) {
	// Single merged array (modern gh).
	single := `[{"id":1,"user":{"login":"alice","type":"User"},"body":"a","path":"x.go","line":1}]`
	// Concatenated arrays, one per page (older gh --paginate).
	concat := `[{"id":1,"user":{"login":"a","type":"User"},"body":"a"}]` +
		`[{"id":2,"user":{"login":"gemini","type":"Bot"},"body":"b"}]`

	got, err := decodeReviewComments([]byte(single))
	if err != nil || len(got) != 1 || got[0].Author.Login != "alice" || got[0].Line != 1 {
		t.Errorf("single: got %+v err %v", got, err)
	}

	got, err = decodeReviewComments([]byte(concat))
	if err != nil || len(got) != 2 || got[1].Author.Login != "gemini" {
		t.Errorf("concatenated: got %+v err %v", got, err)
	}

	got, err = decodeReviewComments([]byte(""))
	if err != nil || len(got) != 0 {
		t.Errorf("empty: got %+v err %v", got, err)
	}
}

func TestExtractOwnerRepo(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"git@github.com:me/auth.git", "me/auth", false},
		{"git@github.com:me/auth", "me/auth", false},
		{"https://github.com/me/auth.git", "me/auth", false},
		{"https://github.com/me/auth", "me/auth", false},
		{"http://github.com/me/auth", "me/auth", false},
		{"me/auth", "me/auth", false},
		{"  me/auth  ", "me/auth", false},

		{"garbage", "", true},
		{"git@github.com:", "", true},
		{"https://github.com/just-owner", "", true},
		{"https://github.com/a/b/c", "", true},
		{"/leading/slash", "", true},
	}
	for _, c := range cases {
		got, err := ExtractOwnerRepo(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ExtractOwnerRepo(%q): err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("ExtractOwnerRepo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReviewCommentsAPIPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://github.com/me/auth/pull/12", "repos/me/auth/pulls/12/comments", false},
		{"https://github.com/me/auth/pull/12/files", "repos/me/auth/pulls/12/comments", false},
		{"https://github.com/me/auth/issues/12", "", true},
		{"https://github.com/me/auth", "", true},
		{"https://github.com/me", "", true},
	}
	for _, c := range cases {
		got, err := reviewCommentsAPIPath(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("reviewCommentsAPIPath(%q): err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("reviewCommentsAPIPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPullCommentReactionAPIPath(t *testing.T) {
	cases := []struct {
		url     string
		id      string
		want    string
		wantErr bool
	}{
		{"https://github.com/me/auth/pull/12", "999", "repos/me/auth/pulls/comments/999/reactions", false},
		{"https://github.com/me/auth/pull/12/files", "1", "repos/me/auth/pulls/comments/1/reactions", false},
		{"https://github.com/me/auth/pull/12", "", "", true},
		{"https://github.com/me/auth/issues/12", "5", "", true},
		{"https://github.com/me", "5", "", true},
	}
	for _, c := range cases {
		got, err := pullCommentReactionAPIPath(c.url, c.id)
		if (err != nil) != c.wantErr {
			t.Errorf("pullCommentReactionAPIPath(%q,%q): err=%v wantErr=%v", c.url, c.id, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("pullCommentReactionAPIPath(%q,%q) = %q, want %q", c.url, c.id, got, c.want)
		}
	}
}

func TestParseActionsRunURL(t *testing.T) {
	cases := []struct {
		in               string
		owner, repo, run string
		ok               bool
	}{
		{"https://github.com/me/auth/actions/runs/123/job/456", "me", "auth", "123", true},
		{"https://github.com/me/auth/actions/runs/789", "me", "auth", "789", true},
		{"https://github.com/me/auth/pull/12", "", "", "", false},
		{"https://circleci.com/gh/me/auth/123", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, c := range cases {
		o, r, run, ok := parseActionsRunURL(c.in)
		if ok != c.ok || o != c.owner || r != c.repo || run != c.run {
			t.Errorf("parseActionsRunURL(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.in, o, r, run, ok, c.owner, c.repo, c.run, c.ok)
		}
	}
}

func TestCheckHelpers(t *testing.T) {
	// Completed failing CheckRun.
	fail := Check{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", DetailsURL: "u"}
	if !fail.IsComplete() || !fail.IsFailure() || fail.CheckName() != "build" || fail.URL() != "u" {
		t.Errorf("failing CheckRun: %+v", fail)
	}
	// In-progress check is not complete.
	if (Check{Name: "x", Status: "IN_PROGRESS"}).IsComplete() {
		t.Error("IN_PROGRESS should not be complete")
	}
	// Passing CheckRun.
	if (Check{Status: "COMPLETED", Conclusion: "SUCCESS"}).IsFailure() {
		t.Error("SUCCESS should not be a failure")
	}
	// Legacy StatusContext failure.
	sc := Check{Context: "ci/legacy", State: "FAILURE", TargetURL: "t"}
	if !sc.IsComplete() || !sc.IsFailure() || sc.CheckName() != "ci/legacy" || sc.URL() != "t" {
		t.Errorf("StatusContext: %+v", sc)
	}
	// Pending StatusContext is not complete.
	if (Check{Context: "x", State: "PENDING"}).IsComplete() {
		t.Error("PENDING StatusContext should not be complete")
	}
}

func TestActorIsBot(t *testing.T) {
	if (Actor{Type: "Bot"}).IsBot() != true {
		t.Error("Bot type should report IsBot true")
	}
	if (Actor{Type: "User"}).IsBot() != false {
		t.Error("User type should report IsBot false")
	}
}

func TestDetailsIsOpen(t *testing.T) {
	if !(Details{State: "OPEN"}).IsOpen() {
		t.Error("State=OPEN should be open")
	}
	if (Details{State: "CLOSED"}).IsOpen() {
		t.Error("State=CLOSED should not be open")
	}
	if (Details{State: "MERGED"}).IsOpen() {
		t.Error("State=MERGED should not be open")
	}
}

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
		number      int
		wantErr     bool
	}{
		{"https://github.com/me/auth/pull/42", "me", "auth", 42, false},
		{"https://github.com/org/repo/pull/1", "org", "repo", 1, false},
		{"https://github.com/me/auth/pull/12/files", "me", "auth", 12, false},

		{"https://github.com/me/auth/issues/12", "", "", 0, true},
		{"https://github.com/me/auth", "", "", 0, true},
		{"https://github.com/me", "", "", 0, true},
		{"https://github.com/me/auth/pull/abc", "", "", 0, true},
		{"", "", "", 0, true},
	}
	for _, c := range cases {
		owner, repo, number, err := parsePRURL(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parsePRURL(%q): err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if owner != c.owner || repo != c.repo || number != c.number {
			t.Errorf("parsePRURL(%q) = (%q, %q, %d), want (%q, %q, %d)",
				c.in, owner, repo, number, c.owner, c.repo, c.number)
		}
	}
}

func TestDecodeAuthorPage(t *testing.T) {
	data := []byte(`{
  "data": {
    "repository": {
      "pullRequest": {
        "comments": {
          "pageInfo": {"hasNextPage": true, "endCursor": "CUR2"},
          "nodes": [
            {"author": {"login": "vercel", "__typename": "Bot"}},
            {"author": {"login": "ahmadAlMezaal", "__typename": "User"}}
          ]
        }
      }
    }
  }
}`)
	nodes, hasNext, end, err := decodeAuthorPage(data, "comments")
	if err != nil {
		t.Fatalf("decodeAuthorPage: %v", err)
	}
	if !hasNext || end != "CUR2" {
		t.Errorf("paging: hasNext=%v end=%q, want true/CUR2", hasNext, end)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	if nodes[0].Login != "vercel" || nodes[0].Typename != "Bot" {
		t.Errorf("node[0] = %+v", nodes[0])
	}
	if nodes[1].Typename != "User" {
		t.Errorf("node[1] should be a User, got %+v", nodes[1])
	}
}

func TestDecodeAuthorPage_LastPageWrongField(t *testing.T) {
	// No next page, and querying a field absent from the response yields nothing.
	data := []byte(`{"data":{"repository":{"pullRequest":{"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}}}`)
	nodes, hasNext, _, err := decodeAuthorPage(data, "comments")
	if err != nil {
		t.Fatalf("decodeAuthorPage: %v", err)
	}
	if hasNext || len(nodes) != 0 {
		t.Errorf("got hasNext=%v nodes=%d, want false/0", hasNext, len(nodes))
	}
}

func TestDecodeReviewThreads(t *testing.T) {
	data := []byte(`{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {
          "nodes": [
            {
              "id": "PRRT_thread1",
              "isResolved": false,
              "comments": {"nodes": [{"databaseId": 100}]}
            },
            {
              "id": "PRRT_thread2",
              "isResolved": true,
              "comments": {"nodes": [{"databaseId": 200}]}
            },
            {
              "id": "PRRT_thread3",
              "isResolved": false,
              "comments": {"nodes": [{"databaseId": 300}]}
            }
          ]
        }
      }
    }
  }
}`)
	threads, err := decodeReviewThreads(data)
	if err != nil {
		t.Fatalf("decodeReviewThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2 (only unresolved)", len(threads))
	}
	if threads[0].ID != "PRRT_thread1" || threads[0].FirstCommentDatabaseID != 100 {
		t.Errorf("thread[0] = %+v", threads[0])
	}
	if threads[1].ID != "PRRT_thread3" || threads[1].FirstCommentDatabaseID != 300 {
		t.Errorf("thread[1] = %+v", threads[1])
	}
}

func TestDecodeReviewThreads_Empty(t *testing.T) {
	data := []byte(`{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {"nodes": []}
      }
    }
  }
}`)
	threads, err := decodeReviewThreads(data)
	if err != nil {
		t.Fatalf("decodeReviewThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("got %d threads, want 0", len(threads))
	}
}

func TestDecodeReviewThreads_NoComments(t *testing.T) {
	data := []byte(`{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {
          "nodes": [
            {
              "id": "PRRT_orphan",
              "isResolved": false,
              "comments": {"nodes": []}
            }
          ]
        }
      }
    }
  }
}`)
	threads, err := decodeReviewThreads(data)
	if err != nil {
		t.Fatalf("decodeReviewThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("got %d threads, want 1", len(threads))
	}
	if threads[0].FirstCommentDatabaseID != 0 {
		t.Errorf("expected 0 comment ID for orphan thread, got %d", threads[0].FirstCommentDatabaseID)
	}
}

// fakeGHThreadCalls installs a fake `gh` on PATH that records each invocation
// (delimited by @@@, since GraphQL queries contain newlines) and returns one
// unresolved review thread. The returned func parses the recorded calls.
func fakeGHThreadCalls(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "gh-calls.log")

	gh := filepath.Join(dir, "gh")
	script := `#!/bin/sh
printf '%s\n@@@\n' "$*" >> ` + logFile + `
case "$*" in
  *reviewThreads*)
    cat <<'JSON'
{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {
          "nodes": [
            {
              "id": "PRRT_abc",
              "isResolved": false,
              "comments": {"nodes": [{"databaseId": 42}]}
            },
            {
              "id": "PRRT_def",
              "isResolved": true,
              "comments": {"nodes": [{"databaseId": 99}]}
            }
          ]
        }
      }
    }
  }
}
JSON
    exit 0
    ;;
esac
echo "{}"
`
	if err := os.WriteFile(gh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		log, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		var calls []string
		for _, e := range strings.Split(strings.TrimSpace(string(log)), "@@@") {
			if s := strings.TrimSpace(e); s != "" {
				calls = append(calls, s)
			}
		}
		return calls
	}
}

func TestReplyToThreads_Resolve(t *testing.T) {
	readCalls := fakeGHThreadCalls(t)

	New().ReplyToThreads(context.Background(), "https://github.com/me/repo/pull/7", "Addressed in abc1234.", true)

	calls := readCalls()
	// Expect 3 calls: 1 GraphQL fetch + 1 REST reply + 1 GraphQL resolve.
	// (The already-resolved thread PRRT_def is skipped.)
	if len(calls) != 3 {
		t.Fatalf("expected 3 gh calls, got %d:\n%v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "api graphql") || !strings.Contains(calls[0], "reviewThreads") {
		t.Errorf("call 1: expected GraphQL thread fetch, got: %s", calls[0])
	}
	if !strings.Contains(calls[1], "repos/me/repo/pulls/7/comments/42/replies") {
		t.Errorf("call 2: expected REST reply, got: %s", calls[1])
	}
	if !strings.Contains(calls[1], "Addressed in abc1234") {
		t.Errorf("call 2: expected SHA in reply body, got: %s", calls[1])
	}
	if !strings.Contains(calls[2], "resolveReviewThread") || !strings.Contains(calls[2], "PRRT_abc") {
		t.Errorf("call 3: expected GraphQL resolve, got: %s", calls[2])
	}
}

func TestReplyToThreads_NoResolve(t *testing.T) {
	readCalls := fakeGHThreadCalls(t)

	New().ReplyToThreads(context.Background(), "https://github.com/me/repo/pull/7", "Addressed in abc1234.", false)

	calls := readCalls()
	// Expect 2 calls: 1 GraphQL fetch + 1 REST reply. No resolve when resolve=false.
	if len(calls) != 2 {
		t.Fatalf("expected 2 gh calls, got %d:\n%v", len(calls), calls)
	}
	if !strings.Contains(calls[1], "repos/me/repo/pulls/7/comments/42/replies") {
		t.Errorf("call 2: expected REST reply, got: %s", calls[1])
	}
	for _, c := range calls {
		if strings.Contains(c, "resolveReviewThread") {
			t.Errorf("expected no resolve call when resolve=false, got: %s", c)
		}
	}
}
