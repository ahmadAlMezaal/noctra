package notify

import (
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
)

func TestEscapeMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"snake_case_thing", `snake\_case\_thing`},
		{"*emphasis*", `\*emphasis\*`},
		{"`code`", "\\`code\\`"},
		{"[link]", `\[link]`},
		{"feat: implement ENG-42 — add_user.go", `feat: implement ENG-42 — add\_user.go`},
		{`already\_escaped`, `already\\_escaped`},
		{"Nothing special here", "Nothing special here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := EscapeMarkdown(c.in); got != c.want {
			t.Errorf("EscapeMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactURLErrorHidesBotToken(t *testing.T) {
	const token = "8606471720:AAtest-secret"
	err := &url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot" + token + "/getUpdates?offset=0",
		Err: io.ErrUnexpectedEOF,
	}

	got := RedactURLError(err, token)

	if strings.Contains(got.Error(), token) {
		t.Errorf("error still contains the bot token: %s", got)
	}
	if !strings.Contains(got.Error(), "bot<redacted>/getUpdates") {
		t.Errorf("error lost the endpoint context: %s", got)
	}
	if !errors.Is(got, io.ErrUnexpectedEOF) {
		t.Error("redaction broke the error chain")
	}
}

func TestRedactURLErrorLeavesOtherErrorsAlone(t *testing.T) {
	plain := errors.New("telegram returned 500")
	if got := RedactURLError(plain, "secret"); got != plain {
		t.Errorf("got %v, want the original error", got)
	}
	if got := RedactURLError(nil, "secret"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}
