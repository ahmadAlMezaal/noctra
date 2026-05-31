package github

import "testing"

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
