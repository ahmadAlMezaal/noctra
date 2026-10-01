package ghauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type fakeService struct {
	t       *testing.T
	pub     ed25519.PublicKey
	nonces  map[string]bool
	tokens  int
	unlinks int
}

func (f *fakeService) verify(r *http.Request, body []byte, instanceID string) bool {
	ts, _ := strconv.ParseInt(r.Header.Get(headerTimestamp), 10, 64)
	nonce := r.Header.Get(headerNonce)
	if f.nonces[nonce] {
		return false
	}
	f.nonces[nonce] = true
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(headerSignature))
	if err != nil || r.Header.Get(headerInstance) != instanceID {
		return false
	}
	return ed25519.Verify(f.pub, []byte(CanonicalString(r.Method, r.URL.Path, ts, nonce, instanceID, body)), sig)
}

func (f *fakeService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/config":
		_, _ = w.Write([]byte(`{"app_id":5151968,"client_id":"Iv23test","slug":"noctra-agent","install_url":"https://github.com/apps/noctra-agent/installations/new","bot":{"login":"noctra-agent[bot]","id":336615789,"email":"336615789+noctra-agent[bot]@users.noreply.github.com"}}`))
	case "/link":
		var req struct {
			GitHubToken string `json:"github_token"`
			PublicKey   string `json:"public_key"`
		}
		_ = json.Unmarshal(body, &req)
		raw, _ := base64.StdEncoding.DecodeString(req.PublicKey)
		f.pub = ed25519.PublicKey(raw)
		if !f.verify(r, body, "") || req.GitHubToken != "ghu_user" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"bad_signature","message":"nope"}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"instance_id":"ni_linked000000000000","github_user":{"id":1001,"login":"alice"},"installations":[{"id":7,"account":"onelastcommit"}],"app":{"slug":"noctra-agent","bot":{"login":"noctra-agent[bot]","id":336615789,"email":"e"}}}`))
	case "/token":
		if !f.verify(r, body, "ni_linked000000000000") {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"replayed_request"}`))
			return
		}
		var req struct{ Repository, Scope string }
		_ = json.Unmarshal(body, &req)
		if req.Repository == "o/missing" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not_installed","message":"not installed","install_url":"https://github.com/apps/noctra-agent/installations/new"}`))
			return
		}
		f.tokens++
		_, _ = w.Write([]byte(`{"token":"ghs_` + strconv.Itoa(f.tokens) + `","expires_at":"2026-10-01T13:00:00Z","repository":"` + req.Repository + `","scope":"` + req.Scope + `"}`))
	case "/unlink":
		if !f.verify(r, body, "ni_linked000000000000") {
			w.WriteHeader(401)
			return
		}
		f.unlinks++
		_, _ = w.Write([]byte(`{"unlinked":true}`))
	case "/healthz":
		_, _ = w.Write([]byte(`{"ok":true}`))
	default:
		w.WriteHeader(404)
	}
}

func newFakeService(t *testing.T) (*fakeService, *Service) {
	f := &fakeService{t: t, nonces: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, NewService(srv.URL + "/")
}

func TestService_LinkTokenUnlink(t *testing.T) {
	ctx := context.Background()
	f, svc := newFakeService(t)
	_, key, _ := NewKey()

	cfg, err := svc.Config(ctx)
	if err != nil || cfg.ClientID != "Iv23test" || cfg.Bot.ID != 336615789 {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	res, err := svc.Link(ctx, "ghu_user", key)
	if err != nil {
		t.Fatal(err)
	}
	if res.InstanceID != "ni_linked000000000000" || res.GitHubUser.Login != "alice" || len(res.Installations) != 1 {
		t.Fatalf("link result: %+v", res)
	}
	tok, err := svc.Token(ctx, res.InstanceID, key, "onelastcommit/noctra", ScopeGit)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Token != "ghs_1" || tok.Scope != ScopeGit || !tok.ExpiresAt.Equal(time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("token: %+v", tok)
	}
	if err := svc.Unlink(ctx, res.InstanceID, key); err != nil || f.unlinks != 1 {
		t.Fatalf("unlink: %v (unlinks=%d)", err, f.unlinks)
	}
	if err := svc.Health(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestService_DecodesErrors(t *testing.T) {
	ctx := context.Background()
	_, svc := newFakeService(t)
	_, key, _ := NewKey()
	if _, err := svc.Link(ctx, "ghu_user", key); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Token(ctx, "ni_linked000000000000", key, "o/missing", ScopeRead)
	if !IsServiceError(err, "not_installed") {
		t.Fatalf("want not_installed, got %v", err)
	}
	se := err.(*ServiceError)
	if se.Status != 404 || se.InstallURL == "" {
		t.Fatalf("unexpected error fields: %+v", se)
	}
	_, wrongKey, _ := NewKey()
	if _, err := svc.Token(ctx, "ni_linked000000000000", wrongKey, "o/r", ScopeRead); !IsServiceError(err, "replayed_request") {
		t.Fatalf("wrong key should be refused, got %v", err)
	}
}

func TestParseScope(t *testing.T) {
	for _, s := range []string{"write", "git", "read"} {
		if _, err := ParseScope(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	if _, err := ParseScope("admin"); err == nil {
		t.Error("admin should be rejected")
	}
}
