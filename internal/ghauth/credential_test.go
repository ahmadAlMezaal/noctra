package ghauth

import (
	"strings"
	"testing"
	"time"
)

func TestParseCredentialInput_StopsAtBlankLine(t *testing.T) {
	attrs, err := ParseCredentialInput(strings.NewReader("protocol=https\nhost=github.com\npath=o/r.git\nnovalue\n\nhost=evil.example\n"))
	if err != nil {
		t.Fatal(err)
	}
	if attrs["host"] != "github.com" || attrs["path"] != "o/r.git" || attrs["protocol"] != "https" {
		t.Fatalf("unexpected attrs: %v", attrs)
	}
}

func TestRepoFromCredential(t *testing.T) {
	cases := []struct {
		attrs   map[string]string
		want    string
		wantErr bool
	}{
		{map[string]string{"protocol": "https", "host": "github.com", "path": "onelastcommit/noctra.git"}, "onelastcommit/noctra", false},
		{map[string]string{"protocol": "https", "host": "GitHub.com", "path": "/o/r"}, "o/r", false},
		{map[string]string{"protocol": "https", "host": "github.com", "path": "o/r.git/info/lfs"}, "o/r", false},
		{map[string]string{"protocol": "https", "host": "github.com"}, "", true},
		{map[string]string{"protocol": "https", "host": "gitlab.com", "path": "o/r"}, "", true},
		{map[string]string{"protocol": "http", "host": "github.com", "path": "o/r"}, "", true},
		{map[string]string{"protocol": "https", "host": "github.com", "path": "o"}, "", true},
	}
	for _, c := range cases {
		got, err := RepoFromCredential(c.attrs)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("RepoFromCredential(%v) = %q, %v; want %q, err=%v", c.attrs, got, err, c.want, c.wantErr)
		}
	}
}

func TestFormatCredential(t *testing.T) {
	got := FormatCredential("ghs_x", time.Unix(1790003600, 0))
	want := "username=x-access-token\npassword=ghs_x\npassword_expiry_utc=1790003600\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(FormatCredential("ghs_x", time.Time{}), "expiry") {
		t.Fatal("zero expiry must be omitted")
	}
}
