package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOAuthRefreshConfigured(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
LINEAR_OAUTH_CLIENT_ID="cid"
LINEAR_OAUTH_CLIENT_SECRET="secret"
LINEAR_OAUTH_REFRESH_TOKEN="refresh"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.OAuthRefreshConfigured() {
		t.Fatal("OAuthRefreshConfigured = false, want true")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("full triplet should validate, got: %v", err)
	}
}

func TestOAuthRefresh_PartialTripletRejected(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	// Client id + secret but no refresh token — can't refresh, must be rejected.
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
LINEAR_OAUTH_CLIENT_ID="cid"
LINEAR_OAUTH_CLIENT_SECRET="secret"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OAuthRefreshConfigured() {
		t.Error("OAuthRefreshConfigured = true on partial triplet, want false")
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "LINEAR_OAUTH_CLIENT_ID") {
		t.Fatalf("expected all-or-none triplet error, got %v", err)
	}
}

func TestOAuthRefresh_TripletAloneSatisfiesAuth(t *testing.T) {
	isolateEnv(t)

	// Refresh triplet with no personal API key is a valid Linear auth.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY=""
LINEAR_OAUTH_CLIENT_ID="cid"
LINEAR_OAUTH_CLIENT_SECRET="secret"
LINEAR_OAUTH_REFRESH_TOKEN="refresh"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("triplet-only setup should validate, got: %v", err)
	}
}
