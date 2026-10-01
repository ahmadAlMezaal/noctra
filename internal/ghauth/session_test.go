package ghauth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type countingMinter struct {
	calls   int
	expires time.Time
	err     error
}

func (m *countingMinter) Token(_ context.Context, _ string, _ ed25519.PrivateKey, ownerRepo string, scope Scope) (Token, error) {
	m.calls++
	if m.err != nil {
		return Token{}, m.err
	}
	return Token{Token: "ghs_" + string(scope) + "_" + strings.Repeat("x", m.calls), ExpiresAt: m.expires, Repository: ownerRepo, Scope: scope}, nil
}

func TestSession_CachesUntilRefreshMargin(t *testing.T) {
	now := time.Unix(1790000000, 0)
	m := &countingMinter{expires: now.Add(time.Hour)}
	s := newSession(sampleInstance(), nil, "/d", "/bin/noctra", m, func() time.Time { return now })
	ctx := context.Background()

	a, _ := s.Token(ctx, "O/R", ScopeWrite)
	b, _ := s.Token(ctx, "o/r", ScopeWrite)
	if m.calls != 1 || a.Token != b.Token {
		t.Fatalf("expected one mint shared case-insensitively, calls=%d", m.calls)
	}
	if _, _ = s.Token(ctx, "o/r", ScopeRead); m.calls != 2 {
		t.Fatalf("a different scope must mint separately, calls=%d", m.calls)
	}
	now = now.Add(51 * time.Minute)
	if _, _ = s.Token(ctx, "o/r", ScopeWrite); m.calls != 3 {
		t.Fatalf("token within the refresh margin must be re-minted, calls=%d", m.calls)
	}
	if _, _ = s.FreshToken(ctx, "o/r", ScopeWrite); m.calls != 4 {
		t.Fatalf("FreshToken must bypass the cache, calls=%d", m.calls)
	}
}

func TestSession_ProcessEnvUsesGitScopeAndBotIdentity(t *testing.T) {
	s := newSession(sampleInstance(), nil, "/home/a/.noctra/github", "/usr/local/bin/noctra", &countingMinter{}, time.Now)
	env := s.ProcessEnv()
	for _, want := range []string{
		"GIT_CONFIG_VALUE_1=!'/usr/local/bin/noctra' git-credential --dir '/home/a/.noctra/github' --scope git",
		"GIT_AUTHOR_NAME=noctra-agent[bot]",
		"GIT_TERMINAL_PROMPT=0",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestSession_AgentEnvIsReadOnly(t *testing.T) {
	m := &countingMinter{expires: time.Now().Add(time.Hour)}
	s := newSession(sampleInstance(), nil, "/d", "/bin/noctra", m, time.Now)
	env := s.AgentEnv(context.Background(), "o/r")
	if !slices.Contains(env, "GH_TOKEN=ghs_read_x") || !slices.Contains(env, "GITHUB_TOKEN=ghs_read_x") {
		t.Fatalf("agent should get a read-scope token: %v", env)
	}
	if !slices.Contains(env, "GIT_CONFIG_VALUE_1=!'/bin/noctra' git-credential --dir '/d' --scope read") {
		t.Fatalf("agent git helper must use read scope: %v", env)
	}
}

func TestSession_AgentEnvNeverFallsBackToAmbientCredentials(t *testing.T) {
	s := newSession(sampleInstance(), nil, "/d", "/bin/noctra", &countingMinter{err: errors.New("down")}, time.Now)
	for _, repo := range []string{"o/r", ""} {
		env := s.AgentEnv(context.Background(), repo)
		if !slices.Contains(env, "GH_TOKEN="+unavailableToken) {
			t.Fatalf("repo %q: want the unavailable sentinel so gh cannot fall back to personal auth, got %v", repo, env)
		}
	}
}
