package github

import "testing"

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
