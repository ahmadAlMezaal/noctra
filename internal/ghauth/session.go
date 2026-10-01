package ghauth

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const refreshMargin = 10 * time.Minute

const unavailableToken = "noctra-token-unavailable"

type minter interface {
	Token(ctx context.Context, instanceID string, key ed25519.PrivateKey, ownerRepo string, scope Scope) (Token, error)
}

type Session struct {
	Instance Instance
	Dir      string
	Exe      string

	key     ed25519.PrivateKey
	service minter
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]Token
}

func OpenSession(dir, exe string) (*Session, error) {
	inst, key, err := Load(dir)
	if err != nil {
		return nil, err
	}
	return newSession(inst, key, dir, exe, NewService(inst.ServiceURL), time.Now), nil
}

func newSession(inst Instance, key ed25519.PrivateKey, dir, exe string, svc minter, now func() time.Time) *Session {
	return &Session{
		Instance: inst,
		Dir:      dir,
		Exe:      exe,
		key:      key,
		service:  svc,
		now:      now,
		cache:    map[string]Token{},
	}
}

func (s *Session) Token(ctx context.Context, ownerRepo string, scope Scope) (Token, error) {
	cacheKey := strings.ToLower(ownerRepo) + "|" + string(scope)
	s.mu.Lock()
	cached, ok := s.cache[cacheKey]
	s.mu.Unlock()
	if ok && s.now().Add(refreshMargin).Before(cached.ExpiresAt) {
		return cached, nil
	}
	tok, err := s.service.Token(ctx, s.Instance.InstanceID, s.key, ownerRepo, scope)
	if err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	s.cache[cacheKey] = tok
	s.mu.Unlock()
	return tok, nil
}

func (s *Session) FreshToken(ctx context.Context, ownerRepo string, scope Scope) (Token, error) {
	return s.service.Token(ctx, s.Instance.InstanceID, s.key, ownerRepo, scope)
}

func (s *Session) GHToken(ctx context.Context, ownerRepo string) (string, error) {
	tok, err := s.Token(ctx, ownerRepo, ScopeWrite)
	return tok.Token, err
}

func (s *Session) ProcessEnv() []string {
	env := GitConfigEnv(HelperCommand(s.Exe, s.Dir, ScopeGit))
	env = append(env, IdentityEnv(s.Instance)...)
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

func (s *Session) Activate() error {
	for _, kv := range s.ProcessEnv() {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}
	return nil
}

func (s *Session) AgentEnv(ctx context.Context, ownerRepo string) []string {
	env := GitConfigEnv(HelperCommand(s.Exe, s.Dir, ScopeRead))
	token := unavailableToken
	if ownerRepo == "" {
		slog.Warn("github app: agent repository unknown; agent runs without GitHub API access")
	} else if tok, err := s.FreshToken(ctx, ownerRepo, ScopeRead); err != nil {
		slog.Warn("github app: could not mint a read token for the agent; it runs without GitHub API access", "repo", ownerRepo, "err", err)
	} else {
		token = tok.Token
	}
	return append(env, "GH_TOKEN="+token, "GITHUB_TOKEN="+token)
}

var active atomic.Pointer[Session]

func SetActive(s *Session) { active.Store(s) }

func Active() *Session { return active.Load() }
