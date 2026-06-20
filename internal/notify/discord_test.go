package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscordSendSync(t *testing.T) {
	var got struct {
		Content string `json:"content"`
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
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := NewDiscord(true, srv.URL)
	if err := d.SendSync(context.Background(), "hello discord"); err != nil {
		t.Fatalf("SendSync: %v", err)
	}
	if got.Content != "hello discord" {
		t.Errorf("got content %q, want %q", got.Content, "hello discord")
	}
}

func TestDiscordSendSyncError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"Invalid Webhook Token"}`))
	}))
	defer srv.Close()

	d := NewDiscord(true, srv.URL)
	err := d.SendSync(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestDiscordTruncatesLongMessages(t *testing.T) {
	var got struct {
		Content string `json:"content"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := NewDiscord(true, srv.URL)
	longMsg := strings.Repeat("x", 3000)
	if err := d.SendSync(context.Background(), longMsg); err != nil {
		t.Fatalf("SendSync: %v", err)
	}
	if len(got.Content) > maxDiscordLen {
		t.Errorf("message not truncated: got %d chars, max %d", len(got.Content), maxDiscordLen)
	}
	if !strings.HasSuffix(got.Content, "...") {
		t.Error("truncated message should end with ...")
	}
}

func TestDiscordDisabled(t *testing.T) {
	d := NewDiscord(false, "https://discord.com/api/webhooks/test")
	if d.Enabled {
		t.Error("expected disabled")
	}
	// Should no-op without panic.
	d.Send(context.Background(), "test")
}

func TestDiscordEmptyWebhook(t *testing.T) {
	d := NewDiscord(true, "")
	if d.Enabled {
		t.Error("expected disabled when webhook URL is empty")
	}
}

func TestDiscordSendSyncNil(t *testing.T) {
	var d *Discord
	err := d.SendSync(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for nil Discord")
	}
}
