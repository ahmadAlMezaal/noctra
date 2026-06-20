package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSlackSendSync(t *testing.T) {
	var got struct {
		Text string `json:"text"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s := NewSlack(true, srv.URL)
	if err := s.SendSync(context.Background(), "hello slack"); err != nil {
		t.Fatalf("SendSync: %v", err)
	}
	if got.Text != "hello slack" {
		t.Errorf("got text %q, want %q", got.Text, "hello slack")
	}
}

func TestSlackSendSyncError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("invalid_token"))
	}))
	defer srv.Close()

	s := NewSlack(true, srv.URL)
	err := s.SendSync(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}

func TestSlackDisabled(t *testing.T) {
	s := NewSlack(false, "https://hooks.slack.com/test")
	if s.Enabled {
		t.Error("expected disabled")
	}
	// Should no-op without panic.
	s.Send(context.Background(), "test")
}

func TestSlackEmptyWebhook(t *testing.T) {
	s := NewSlack(true, "")
	if s.Enabled {
		t.Error("expected disabled when webhook URL is empty")
	}
}

func TestSlackSendSyncNil(t *testing.T) {
	var s *Slack
	err := s.SendSync(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for nil Slack")
	}
}
