package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTokenEndpoint  = "https://api.linear.app/oauth/token"
	tokenRefreshMargin    = 5 * time.Minute
	defaultAccessTokenTTL = 24 * time.Hour
)

// TokenStore persists the rotating OAuth credentials across restarts. Linear
// rotates the refresh token on every exchange, so the persisted copy — not the
// .env seed — is the source of truth after the first refresh. *state.Store
// satisfies this.
type TokenStore interface {
	LoadOAuth() (access string, expiresAt time.Time, refresh string)
	SaveOAuth(access string, expiresAt time.Time, refresh string) error
}

// TokenManagerConfig configures a TokenManager. RefreshToken seeds the manager;
// a persisted Store value overrides it. Endpoint/HTTP are test overrides.
type TokenManagerConfig struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	Endpoint     string
	HTTP         *http.Client
	Store        TokenStore
}

// TokenManager mints and caches short-lived Linear access tokens from a rotating
// refresh token, persisting each rotation via Store. Safe for concurrent use.
type TokenManager struct {
	clientID     string
	clientSecret string
	endpoint     string
	http         *http.Client
	store        TokenStore

	mu        sync.Mutex
	access    string
	expiresAt time.Time
	refresh   string
}

// NewTokenManager builds a TokenManager, preferring a persisted refresh token
// over the config seed.
func NewTokenManager(cfg TokenManagerConfig) *TokenManager {
	m := &TokenManager{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		endpoint:     cfg.Endpoint,
		http:         cfg.HTTP,
		store:        cfg.Store,
		refresh:      strings.TrimSpace(cfg.RefreshToken),
	}
	if m.endpoint == "" {
		m.endpoint = defaultTokenEndpoint
	}
	if m.http == nil {
		m.http = &http.Client{Timeout: 30 * time.Second}
	}
	if m.store != nil {
		if access, exp, refresh := m.store.LoadOAuth(); refresh != "" {
			m.access, m.expiresAt, m.refresh = access, exp, refresh
		}
	}
	return m
}

// Token returns a valid access token, refreshing within tokenRefreshMargin of
// expiry.
func (m *TokenManager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.access != "" && time.Until(m.expiresAt) > tokenRefreshMargin {
		return m.access, nil
	}
	if err := m.refreshLocked(ctx); err != nil {
		return "", err
	}
	return m.access, nil
}

// ForceRefresh refreshes immediately, ignoring the cache. Wired as the client's
// OnAuthError hook for when Linear rejects a token we thought was valid.
func (m *TokenManager) ForceRefresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refreshLocked(ctx)
}

// refreshLocked exchanges the refresh token for a new pair. Caller holds m.mu.
func (m *TokenManager) refreshLocked(ctx context.Context) error {
	if m.refresh == "" {
		return fmt.Errorf("linear oauth: no refresh token available")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", m.refresh)
	form.Set("client_id", m.clientID)
	form.Set("client_secret", m.clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("linear oauth refresh: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("linear oauth refresh: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear oauth refresh: status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return fmt.Errorf("linear oauth refresh: decode: %w", err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("linear oauth refresh: empty access_token")
	}

	m.access = tr.AccessToken
	if tr.ExpiresIn > 0 {
		m.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		m.expiresAt = time.Now().Add(defaultAccessTokenTTL)
	}
	if tr.RefreshToken != "" {
		m.refresh = tr.RefreshToken // Linear rotates it; persist or break next cycle.
	}

	if m.store != nil {
		if err := m.store.SaveOAuth(m.access, m.expiresAt, m.refresh); err != nil {
			slog.Warn("linear oauth: failed to persist rotated token", "err", err)
		}
	}
	return nil
}
