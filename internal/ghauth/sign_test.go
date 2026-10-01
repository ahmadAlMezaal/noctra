package ghauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCanonicalString_MatchesTokenServiceFormat(t *testing.T) {
	body := []byte(`{"repository":"a/b","scope":"read"}`)
	got := CanonicalString("post", "/token", 1790000000, "abcdefghijklmnop", "ni_abcdefghijklmnop", body)
	want := strings.Join([]string{
		"noctra-auth-v1",
		"POST",
		"/token",
		"1790000000",
		"abcdefghijklmnop",
		"ni_abcdefghijklmnop",
		"b3b670f491707da8f452e56c1e10a2452b18bed4f9bb4a6b511c5118ee9dc7b9",
	}, "\n")
	if got != want {
		t.Fatalf("canonical string mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestSignedHeaders_VerifyWithPublicKey(t *testing.T) {
	pub, key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	now := time.Unix(1790000000, 0)
	h := SignedHeaders(key, "ni_abcdefghijklmnop", "POST", "/unlink", body, now, "nonce0000000000001")
	sig, err := base64.StdEncoding.DecodeString(h[headerSignature])
	if err != nil {
		t.Fatal(err)
	}
	msg := CanonicalString("POST", "/unlink", now.Unix(), "nonce0000000000001", "ni_abcdefghijklmnop", body)
	if !ed25519.Verify(pub, []byte(msg), sig) {
		t.Fatal("signature does not verify")
	}
	if h[headerTimestamp] != "1790000000" || h[headerInstance] != "ni_abcdefghijklmnop" {
		t.Fatalf("unexpected headers: %v", h)
	}
}

func TestSignedHeaders_OmitsInstanceOnLink(t *testing.T) {
	_, key, _ := NewKey()
	h := SignedHeaders(key, "", "POST", "/link", []byte(`{}`), time.Now(), NewNonce())
	if _, ok := h[headerInstance]; ok {
		t.Fatal("link requests must not carry an instance header")
	}
}

func TestNewNonce_AcceptedByService(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n := NewNonce()
		if !re.MatchString(n) {
			t.Fatalf("nonce %q would be rejected", n)
		}
		if seen[n] {
			t.Fatal("nonce repeated")
		}
		seen[n] = true
	}
}

func TestEncodePublicKey_Raw32Bytes(t *testing.T) {
	pub, _, _ := NewKey()
	raw, err := base64.StdEncoding.DecodeString(EncodePublicKey(pub))
	if err != nil || len(raw) != 32 {
		t.Fatalf("want 32 raw bytes, got %d (%v)", len(raw), err)
	}
}
