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

	d := NewDiscord(srv.URL)
	if err := d.SendSync(context.Background(), "hello discord"); err != nil {
		t.Fatalf("SendSync: %v", err)
	}
	if got.Content != "hello discord" {
		t.Errorf("got content %q, want %q", got.Content, "hello discord")
	}
}

func TestDiscordRewritesBold(t *testing.T) {
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

	d := NewDiscord(srv.URL)
	if err := d.SendSync(context.Background(), "*Noctra* opened a PR"); err != nil {
		t.Fatalf("SendSync: %v", err)
	}
	if got.Content != "**Noctra** opened a PR" {
		t.Errorf("got content %q, want %q", got.Content, "**Noctra** opened a PR")
	}
}

func TestDiscordSuppressesMentions(t *testing.T) {
	var got struct {
		Content         string `json:"content"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
	if err := d.SendSync(context.Background(), "ticket @everyone title"); err != nil {
		t.Fatalf("SendSync: %v", err)
	}
	// parse must be present and empty ([], not null) so Discord suppresses
	// all @everyone/@here/role/user mentions from untrusted ticket text.
	if got.AllowedMentions.Parse == nil {
		t.Fatal("allowed_mentions.parse missing or null — mentions not suppressed")
	}
	if len(got.AllowedMentions.Parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty", got.AllowedMentions.Parse)
	}
}

func TestToDiscordMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"*bold*", "**bold**"},
		{"plain text", "plain text"},
		{"*a* and *b*", "**a** and **b**"},
		{"no *closing", "no *closing"},
		{"", ""},
	}
	for _, c := range cases {
		if got := toDiscordMarkdown(c.in); got != c.want {
			t.Errorf("toDiscordMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDiscordSendSyncError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Invalid Webhook Token"}`))
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
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

	d := NewDiscord(srv.URL)
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

func TestDiscordDisabledWhenNoURL(t *testing.T) {
	d := NewDiscord("")
	if d.Enabled {
		t.Error("expected disabled when webhook URL is empty")
	}
	// A disabled notifier should no-op without panic.
	d.Send(context.Background(), "test")
}

func TestDiscordEnabledWhenURLSet(t *testing.T) {
	d := NewDiscord("https://discord.com/api/webhooks/test")
	if !d.Enabled {
		t.Error("expected enabled when webhook URL is set")
	}
}

func TestDiscordSendSyncNil(t *testing.T) {
	var d *Discord
	err := d.SendSync(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for nil Discord")
	}
}
