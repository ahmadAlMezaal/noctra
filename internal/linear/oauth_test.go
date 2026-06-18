package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu        sync.Mutex
	access    string
	refresh   string
	expiresAt time.Time
	saves     int
}

func (f *fakeStore) LoadOAuth() (string, time.Time, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.access, f.expiresAt, f.refresh
}

func (f *fakeStore) SaveOAuth(a string, e time.Time, r string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access, f.expiresAt, f.refresh, f.saves = a, e, r, f.saves+1
	return nil
}

// tokenServer returns a server that mints rotating tokens and records the
// refresh_token it last received.
func tokenServer(t *testing.T) (*httptest.Server, *int, *string) {
	t.Helper()
	hits := 0
	lastRefresh := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.FormValue("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		hits++
		lastRefresh = r.FormValue("refresh_token")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-token",
			"refresh_token": "rotated-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &lastRefresh
}

func TestTokenManager_RefreshRotatesAndPersists(t *testing.T) {
	srv, hits, lastRefresh := tokenServer(t)
	store := &fakeStore{}
	m := NewTokenManager(TokenManagerConfig{
		ClientID: "id", ClientSecret: "secret",
		RefreshToken: "seed-refresh", Endpoint: srv.URL, Store: store,
	})

	tok, err := m.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "access-token" {
		t.Errorf("token = %q, want access-token", tok)
	}
	if *lastRefresh != "seed-refresh" {
		t.Errorf("server saw refresh %q, want seed-refresh", *lastRefresh)
	}
	if store.refresh != "rotated-refresh" {
		t.Errorf("persisted refresh = %q, want rotated-refresh", store.refresh)
	}
	if store.saves != 1 {
		t.Errorf("saves = %d, want 1", store.saves)
	}
	if *hits != 1 {
		t.Errorf("server hits = %d, want 1", *hits)
	}
}

func TestTokenManager_CachesUntilMargin(t *testing.T) {
	srv, hits, _ := tokenServer(t)
	m := NewTokenManager(TokenManagerConfig{
		ClientID: "id", ClientSecret: "secret",
		RefreshToken: "seed-refresh", Endpoint: srv.URL,
	})
	if _, err := m.Token(context.Background()); err != nil {
		t.Fatalf("Token #1: %v", err)
	}
	if _, err := m.Token(context.Background()); err != nil {
		t.Fatalf("Token #2: %v", err)
	}
	if *hits != 1 {
		t.Errorf("server hits = %d, want 1 (second call should be cached)", *hits)
	}
}

func TestTokenManager_PrefersPersistedRefresh(t *testing.T) {
	srv, _, lastRefresh := tokenServer(t)
	store := &fakeStore{refresh: "persisted-refresh"}
	m := NewTokenManager(TokenManagerConfig{
		ClientID: "id", ClientSecret: "secret",
		RefreshToken: "seed-refresh", Endpoint: srv.URL, Store: store,
	})
	if _, err := m.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if *lastRefresh != "persisted-refresh" {
		t.Errorf("server saw refresh %q, want persisted-refresh (store overrides seed)", *lastRefresh)
	}
}

func TestTokenManager_RefreshFailureIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	m := NewTokenManager(TokenManagerConfig{
		ClientID: "id", ClientSecret: "secret",
		RefreshToken: "dead", Endpoint: srv.URL,
	})
	if _, err := m.Token(context.Background()); err == nil {
		t.Fatal("Token: want error on refresh failure, got nil")
	}
}

func TestTokenManager_NoRefreshTokenIsError(t *testing.T) {
	m := NewTokenManager(TokenManagerConfig{ClientID: "id", ClientSecret: "secret"})
	if _, err := m.Token(context.Background()); err == nil {
		t.Fatal("Token: want error with no refresh token, got nil")
	}
}

// TestClient_DegradesToAPIKeyOnAuthFailure verifies the graceful fallback: when
// the OAuth credential is rejected, the client retries with FallbackAPIKey and
// fires OnDegrade once instead of failing the call.
func TestClient_DegradesToAPIKeyOnAuthFailure(t *testing.T) {
	const goodKey = "lin_api_good"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == goodKey {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"viewer": map[string]string{"id": "u1", "name": "Ahmad"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]string{{"message": "Authentication required, not authenticated"}},
		})
	}))
	defer srv.Close()

	var degraded int
	c := New(goodKey)
	c.Endpoint = srv.URL
	c.TokenFn = func(context.Context) (string, error) { return "dead-oauth-token", nil }
	c.FallbackAPIKey = goodKey
	c.OnDegrade = func(error) { degraded++ }

	name, err := c.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if name != "Ahmad" {
		t.Errorf("name = %q, want Ahmad", name)
	}
	if degraded != 1 {
		t.Errorf("OnDegrade called %d times, want 1", degraded)
	}

	// Subsequent calls go straight to the key without re-degrading.
	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping #2: %v", err)
	}
	if degraded != 1 {
		t.Errorf("OnDegrade called %d times total, want 1 (latched)", degraded)
	}
}
