package notify

import "testing"

func TestEscapeMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		// The four Telegram-legacy strict chars.
		{"snake_case_thing", `snake\_case\_thing`},
		{"*emphasis*", `\*emphasis\*`},
		{"`code`", "\\`code\\`"},
		{"[link]", `\[link]`},
		// Mixed
		{"feat: implement ENG-42 — add_user.go", `feat: implement ENG-42 — add\_user.go`},
		// Already-escaped sequences re-escape the underscore but leave the
		// previous backslash alone (idempotency isn't a goal — only correct
		// disambiguation for Telegram's parser).
		{`already\_escaped`, `already\\_escaped`},
		// Plain text passes through.
		{"Nothing special here", "Nothing special here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := EscapeMarkdown(c.in); got != c.want {
			t.Errorf("EscapeMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
