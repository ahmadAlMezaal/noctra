package ghauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func deviceServer(t *testing.T, responses []string) (*DeviceFlow, *[]time.Duration) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("missing Accept header")
		}
		switch r.URL.Path {
		case "/login/device/code":
			if r.Form.Get("client_id") != "Iv23test" {
				t.Errorf("client_id = %q", r.Form.Get("client_id"))
			}
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
		case "/login/oauth/access_token":
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.Form.Get("device_code") != "dc" {
				t.Errorf("bad poll form: %v", r.Form)
			}
			_, _ = w.Write([]byte(responses[min(calls, len(responses)-1)]))
			calls++
		}
	}))
	t.Cleanup(srv.Close)
	var slept []time.Duration
	flow := NewDeviceFlow("Iv23test")
	flow.WebURL = srv.URL
	flow.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return flow, &slept
}

func TestDeviceFlow_PendingSlowDownThenToken(t *testing.T) {
	flow, slept := deviceServer(t, []string{
		`{"error":"authorization_pending"}`,
		`{"error":"slow_down","interval":10}`,
		`{"access_token":"ghu_user","token_type":"bearer"}`,
	})
	ctx := context.Background()
	dc, err := flow.Start(ctx)
	if err != nil || dc.UserCode != "ABCD-1234" {
		t.Fatalf("start: %+v %v", dc, err)
	}
	tok, err := flow.Wait(ctx, dc)
	if err != nil || tok != "ghu_user" {
		t.Fatalf("wait: %q %v", tok, err)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}
	if len(*slept) != len(want) {
		t.Fatalf("slept %v, want %v", *slept, want)
	}
	for i := range want {
		if (*slept)[i] != want[i] {
			t.Fatalf("slept %v, want %v", *slept, want)
		}
	}
}

func TestDeviceFlow_Errors(t *testing.T) {
	cases := map[string]string{
		`{"error":"access_denied"}`: "declined",
		`{"error":"expired_token"}`: "expired",
		`{"error":"weird_thing"}`:   "weird_thing",
	}
	for resp, want := range cases {
		flow, _ := deviceServer(t, []string{resp})
		dc, _ := flow.Start(context.Background())
		if _, err := flow.Wait(context.Background(), dc); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want error containing %q, got %v", resp, want, err)
		}
	}
}
