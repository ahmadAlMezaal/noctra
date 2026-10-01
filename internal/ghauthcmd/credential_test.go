package ghauthcmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onelastcommit/noctra/internal/ghauth"
)

func linkedDir(t *testing.T, handler http.HandlerFunc) string {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	_, key, err := ghauth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	inst := ghauth.Instance{InstanceID: "ni_test0000000000000000", ServiceURL: srv.URL, BotLogin: "noctra-agent[bot]", LinkedAt: time.Now()}
	if err := ghauth.Save(dir, inst, key); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGitCredential_ReturnsScopedToken(t *testing.T) {
	var gotRepo, gotScope string
	dir := linkedDir(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Repository, Scope string }
		_ = json.Unmarshal(body, &req)
		gotRepo, gotScope = req.Repository, req.Scope
		_, _ = w.Write([]byte(`{"token":"ghs_scoped","expires_at":"2026-10-01T13:00:00Z","repository":"o/r","scope":"git"}`))
	})
	var out, errOut bytes.Buffer
	in := strings.NewReader("protocol=https\nhost=github.com\npath=onelastcommit/noctra.git\n\n")
	if err := GitCredential([]string{"--dir", dir, "--scope", "git", "get"}, in, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if gotRepo != "onelastcommit/noctra" || gotScope != "git" {
		t.Fatalf("service saw repo=%q scope=%q", gotRepo, gotScope)
	}
	want := "username=x-access-token\npassword=ghs_scoped\npassword_expiry_utc=1790859600\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

func TestGitCredential_IgnoresStoreAndErase(t *testing.T) {
	for _, action := range []string{"store", "erase"} {
		var out bytes.Buffer
		if err := GitCredential([]string{"--dir", "/nonexistent", action}, strings.NewReader("password=x\n\n"), &out, io.Discard); err != nil || out.Len() != 0 {
			t.Errorf("%s: err=%v out=%q", action, err, out.String())
		}
	}
}

func TestGitCredential_ProvidesNothingWhenItCannotHelp(t *testing.T) {
	dir := linkedDir(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"not_installed","message":"nope"}`))
	})
	cases := map[string]string{
		"protocol=https\nhost=gitlab.com\npath=o/r\n\n": "only github.com",
		"protocol=https\nhost=github.com\npath=o/r\n\n": "not_installed",
		"protocol=https\nhost=github.com\n\n":           "useHttpPath",
	}
	for input, wantErr := range cases {
		var out, errOut bytes.Buffer
		if err := GitCredential([]string{"--dir", dir, "get"}, strings.NewReader(input), &out, &errOut); err != nil {
			t.Fatalf("helper must not fail git outright: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("%q: expected no credentials, got %q", input, out.String())
		}
		if !strings.Contains(errOut.String(), wantErr) {
			t.Errorf("%q: stderr %q should mention %q", input, errOut.String(), wantErr)
		}
	}
}

func TestGitCredential_RejectsUnknownScope(t *testing.T) {
	err := GitCredential([]string{"--dir", t.TempDir(), "--scope", "admin", "get"}, strings.NewReader("\n"), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("unknown scope must be an error")
	}
}
