package repo

import "testing"

func TestSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Auth Service", "auth-service"},
		{"My API   v2", "my-api-v2"},
		{"Nightshift!", "nightshift"},
		{"  leading/trailing  ", "leading-trailing"},
		{"!@#$", ""},
		{"", ""},
		{"ALREADY-fine", "already-fine"},
	}
	for _, c := range cases {
		if got := Slug(c.in); got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
