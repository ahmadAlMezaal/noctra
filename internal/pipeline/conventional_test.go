package pipeline

import "testing"

func TestConventionalType(t *testing.T) {
	cases := []struct {
		bump     string
		wantType string
		wantBrk  bool
	}{
		{"patch", "fix", false},
		{"minor", "feat", false},
		{"major", "feat", true},
		{"none", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		typ, brk := conventionalType(c.bump)
		if typ != c.wantType || brk != c.wantBrk {
			t.Errorf("conventionalType(%q) = (%q,%v), want (%q,%v)", c.bump, typ, brk, c.wantType, c.wantBrk)
		}
	}
}

func TestConventionalSubject(t *testing.T) {
	if got := conventionalSubject("fix", false, "fix the bug", "ENG-7"); got != "fix: fix the bug (ENG-7)" {
		t.Errorf("got %q", got)
	}
	if got := conventionalSubject("feat", true, "drop v1 API", "ENG-9"); got != "feat!: drop v1 API (ENG-9)" {
		t.Errorf("got %q", got)
	}
}
