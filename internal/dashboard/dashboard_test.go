package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type testSnapshot struct {
	Active []string `json:"active"`
	OK     bool     `json:"ok"`
}

func newTestServer(token string) *Server {
	return New(":0", token, func() any {
		return testSnapshot{Active: []string{"ENG-1"}, OK: true}
	})
}

func TestAuth_BearerToken(t *testing.T) {
	s := newTestServer("secret-token")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Valid Bearer token → 200.
	req, _ := http.NewRequest("GET", ts.URL+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var snap testSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if !snap.OK || len(snap.Active) != 1 || snap.Active[0] != "ENG-1" {
		t.Errorf("unexpected snapshot: %+v", snap)
	}
}

func TestAuth_QueryParam(t *testing.T) {
	s := newTestServer("qp-token")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/snapshot?token=qp-token")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAuth_Missing(t *testing.T) {
	s := newTestServer("secret")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuth_WrongToken(t *testing.T) {
	s := newTestServer("correct")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuth_StaticPage(t *testing.T) {
	s := newTestServer("page-token")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Page load without token → 401.
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 for page without token, got %d", resp.StatusCode)
	}

	// Page load with query param token → 200.
	resp, err = http.Get(ts.URL + "/?token=page-token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 for page with token, got %d", resp.StatusCode)
	}
}

func TestSnapshot_MethodNotAllowed(t *testing.T) {
	s := newTestServer("tok")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/snapshot", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}
