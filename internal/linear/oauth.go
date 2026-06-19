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
	defaultOAuthScope     = "read,write"
)

type TokenStore interface {
	LoadOAuth() (access string, expiresAt time.Time, refresh string)
	SaveOAuth(access string, expiresAt time.Time, refresh string) error
}

type TokenManagerConfig struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	Scope        string
	Endpoint     string
	HTTP         *http.Client
	Store        TokenStore
}

type TokenManager struct {
	clientID     string
	clientSecret string
	scope        string
	endpoint     string
	http         *http.Client
	store        TokenStore

	mu        sync.Mutex
	access    string
	expiresAt time.Time
	refresh   string
}

func NewTokenManager(cfg TokenManagerConfig) *TokenManager {
	m := &TokenManager{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		scope:        cfg.Scope,
		endpoint:     cfg.Endpoint,
		http:         cfg.HTTP,
		store:        cfg.Store,
		refresh:      strings.TrimSpace(cfg.RefreshToken),
	}
	if m.scope == "" {
		m.scope = defaultOAuthScope
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

func (m *TokenManager) ForceRefresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refreshLocked(ctx)
}

func (m *TokenManager) refreshLocked(ctx context.Context) error {
	form := url.Values{}
	form.Set("client_id", m.clientID)
	form.Set("client_secret", m.clientSecret)
	switch {
	case m.refresh != "":
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", m.refresh)
	case m.clientID != "" && m.clientSecret != "":
		form.Set("grant_type", "client_credentials")
		form.Set("actor", "app")
		form.Set("scope", m.scope)
	default:
		return fmt.Errorf("linear oauth: no client credentials or refresh token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("linear oauth: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("linear oauth: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear oauth: status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return fmt.Errorf("linear oauth: decode: %w", err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("linear oauth: empty access_token")
	}

	m.access = tr.AccessToken
	if tr.ExpiresIn > 0 {
		m.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		m.expiresAt = time.Now().Add(defaultAccessTokenTTL)
	}
	if tr.RefreshToken != "" {
		m.refresh = tr.RefreshToken
	}

	if m.store != nil {
		if err := m.store.SaveOAuth(m.access, m.expiresAt, m.refresh); err != nil {
			slog.Warn("linear oauth: failed to persist token", "err", err)
		}
	}
	return nil
}
