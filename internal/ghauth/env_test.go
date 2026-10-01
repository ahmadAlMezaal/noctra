package ghauth

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		setting string
		linked  bool
		want    Mode
		wantErr bool
	}{
		{"", false, ModeToken, false},
		{"auto", false, ModeToken, false},
		{"auto", true, ModeApp, false},
		{" AUTO ", true, ModeApp, false},
		{"app", true, ModeApp, false},
		{"app", false, "", true},
		{"token", true, ModeToken, false},
		{"token", false, ModeToken, false},
		{"bogus", true, "", true},
	}
	for _, c := range cases {
		got, err := ResolveMode(c.setting, c.linked)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ResolveMode(%q, %v) = %q, %v; want %q, err=%v", c.setting, c.linked, got, err, c.want, c.wantErr)
		}
	}
	if _, err := ResolveMode("app", false); !errors.Is(err, ErrNotLinked) {
		t.Errorf("app mode without a link should wrap ErrNotLinked, got %v", err)
	}
}

func TestHelperCommand_QuotesPaths(t *testing.T) {
	got := HelperCommand("/opt/my apps/noctra", "/home/o'brien/.noctra/github", ScopeRead)
	want := `!'/opt/my apps/noctra' git-credential --dir '/home/o'\''brien/.noctra/github' --scope read`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestGitConfigEnv_ResetsThenSetsHelper(t *testing.T) {
	env := GitConfigEnv("!helper")
	want := []string{
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=!helper",
		"GIT_CONFIG_KEY_2=credential.useHttpPath", "GIT_CONFIG_VALUE_2=true",
	}
	if !slices.Equal(env, want) {
		t.Fatalf("got %v", env)
	}
}

func TestIdentityEnv(t *testing.T) {
	env := IdentityEnv(Instance{BotLogin: "noctra-agent[bot]", BotEmail: "336615789+noctra-agent[bot]@users.noreply.github.com"})
	for _, want := range []string{
		"GIT_AUTHOR_NAME=noctra-agent[bot]",
		"GIT_COMMITTER_NAME=noctra-agent[bot]",
		"GIT_AUTHOR_EMAIL=336615789+noctra-agent[bot]@users.noreply.github.com",
		"GIT_COMMITTER_EMAIL=336615789+noctra-agent[bot]@users.noreply.github.com",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}
}

func TestMergeEnv_OverridesWithoutDuplicates(t *testing.T) {
	base := []string{"PATH=/bin", "GH_TOKEN=personal", "GIT_CONFIG_COUNT=3", "HOME=/h"}
	got := MergeEnv(base, []string{"GH_TOKEN=scoped", "GIT_CONFIG_COUNT=3", "NEW=1"})
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "personal") {
		t.Fatalf("personal token leaked: %v", got)
	}
	for _, want := range []string{"PATH=/bin", "HOME=/h", "GH_TOKEN=scoped", "NEW=1"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if n := strings.Count(joined, "GIT_CONFIG_COUNT="); n != 1 {
		t.Errorf("GIT_CONFIG_COUNT appears %d times", n)
	}
	if got := MergeEnv(base, nil); !slices.Equal(got, base) {
		t.Errorf("no overrides should return base unchanged")
	}
}
