package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadOAuth_RoundTripsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := s.SaveOAuth("access-1", exp, "refresh-1"); err != nil {
		t.Fatalf("SaveOAuth: %v", err)
	}

	// Reopen from disk to prove it persisted.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	access, gotExp, refresh := s2.LoadOAuth()
	if access != "access-1" || refresh != "refresh-1" {
		t.Errorf("LoadOAuth = (%q, %q), want (access-1, refresh-1)", access, refresh)
	}
	if !gotExp.Equal(exp) {
		t.Errorf("expiry = %v, want %v", gotExp, exp)
	}
}

func TestLoadOAuth_EmptyWhenUnset(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if access, exp, refresh := s.LoadOAuth(); access != "" || refresh != "" || !exp.IsZero() {
		t.Errorf("LoadOAuth on empty store = (%q, %v, %q), want zeroes", access, exp, refresh)
	}
}
