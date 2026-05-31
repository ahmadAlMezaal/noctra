package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestListener_AuthRejectsWrongChat(t *testing.T) {
	l := New("tok", "12345")
	var dispatched bool
	l.dispatcher.Register("test", "test", func(_ context.Context, _ string) string {
		dispatched = true
		return ""
	})

	u := Update{
		UpdateID: 1,
		Message: &Message{
			Text: "/test",
			Chat: Chat{ID: 99999}, // wrong chat ID
			From: &User{Username: "attacker"},
		},
	}

	l.handleUpdate(context.Background(), u)
	if dispatched {
		t.Error("handler should not be called for unauthorised chat")
	}
}

func TestListener_AuthAcceptsCorrectChat(t *testing.T) {
	l := New("tok", "12345")
	var dispatched bool
	l.dispatcher.Register("test", "test", func(_ context.Context, _ string) string {
		dispatched = true
		return ""
	})

	u := Update{
		UpdateID: 1,
		Message: &Message{
			Text: "/test",
			Chat: Chat{ID: 12345},
			From: &User{Username: "owner"},
		},
	}

	l.handleUpdate(context.Background(), u)
	if !dispatched {
		t.Error("handler should be called for authorised chat")
	}
}

func TestListener_IgnoresEmptyText(t *testing.T) {
	l := New("tok", "12345")
	var dispatched bool
	l.dispatcher.Register("test", "test", func(_ context.Context, _ string) string {
		dispatched = true
		return ""
	})

	u := Update{
		UpdateID: 1,
		Message: &Message{
			Text: "   ",
			Chat: Chat{ID: 12345},
			From: &User{Username: "owner"},
		},
	}

	l.handleUpdate(context.Background(), u)
	if dispatched {
		t.Error("handler should not be called for empty text")
	}
}

func TestListener_IgnoresNilMessage(t *testing.T) {
	l := New("tok", "12345")
	// Should not panic.
	l.handleUpdate(context.Background(), Update{UpdateID: 1, Message: nil})
}

func TestListener_GetUpdatesParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/getUpdates") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		resp := apiResponse{
			OK: true,
			Result: []Update{
				{UpdateID: 100, Message: &Message{
					Text: "/ping",
					Chat: Chat{ID: 42},
					From: &User{Username: "user"},
				}},
				{UpdateID: 101, Message: &Message{
					Text: "/help",
					Chat: Chat{ID: 42},
					From: &User{Username: "user"},
				}},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	l := New("tok", "42")
	l.pollTimeout = 0
	l.baseURL = srv.URL + "/botTOK"

	updates, err := l.getUpdates(context.Background(), 0)
	if err != nil {
		t.Fatalf("getUpdates: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if updates[0].UpdateID != 100 || updates[1].UpdateID != 101 {
		t.Errorf("unexpected update IDs: %d, %d", updates[0].UpdateID, updates[1].UpdateID)
	}
}

func TestListener_RunProcessesAndStops(t *testing.T) {
	callCount := 0
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/sendMessage") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if !strings.Contains(r.URL.Path, "/getUpdates") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		mu.Lock()
		callCount++
		call := callCount
		mu.Unlock()

		var resp apiResponse
		resp.OK = true

		if call == 1 {
			resp.Result = []Update{
				{UpdateID: 1, Message: &Message{
					Text: "/ping",
					Chat: Chat{ID: 42},
					From: &User{Username: "user"},
				}},
			}
		} else {
			// First update processed; cancel to stop the loop promptly.
			cancel()
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	l := New("tok", "42")
	l.pollTimeout = 0
	l.baseURL = srv.URL + "/botTOK"

	err := l.Run(ctx)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if callCount < 1 {
		t.Error("expected at least one getUpdates call")
	}
}

func TestNextBackoff(t *testing.T) {
	cases := []struct {
		in, want time.Duration
	}{
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{30 * time.Second, 60 * time.Second},
		{60 * time.Second, 60 * time.Second},  // cap
		{120 * time.Second, 60 * time.Second}, // over cap
	}
	for _, c := range cases {
		got := nextBackoff(c.in)
		if got != c.want {
			t.Errorf("nextBackoff(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
