package config

import (
	"path/filepath"
	"testing"
)

func TestActorAppConfigured(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
LINEAR_OAUTH_CLIENT_ID="cid"
LINEAR_OAUTH_CLIENT_SECRET="secret"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.ActorAppConfigured() {
		t.Fatal("ActorAppConfigured = false, want true")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("client id+secret should validate, got: %v", err)
	}
}

func TestActorApp_PartialIsNotFatalWithAPIKey(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY="lin_xyz"
LINEAR_OAUTH_CLIENT_ID="cid"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.OAuthPartiallyConfigured() {
		t.Error("OAuthPartiallyConfigured = false, want true")
	}
	if cfg.ActorAppConfigured() {
		t.Error("ActorAppConfigured = true on partial config, want false")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("partial actor=app config must not be fatal when an API key exists, got: %v", err)
	}
}

func TestActorApp_PairAloneSatisfiesAuth(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY=""
LINEAR_OAUTH_CLIENT_ID="cid"
LINEAR_OAUTH_CLIENT_SECRET="secret"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("client id+secret alone should validate, got: %v", err)
	}
}

func TestActorApp_NoUsableAuthFails(t *testing.T) {
	isolateEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), `LINEAR_API_KEY=""
LINEAR_OAUTH_CLIENT_ID="cid"`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error: no usable Linear auth (partial actor=app, no API key)")
	}
}
