// Package telegram is the inbound (getUpdates long-polling) counterpart to internal/notify:
// it authenticates the sender against TELEGRAM_CHAT_ID and routes commands to handlers.
// Long-polling needs no inbound ports/tunnel/TLS, fitting a Pi behind a home network.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Listener long-polls the Telegram Bot API for inbound messages.
type Listener struct {
	botToken   string
	chatID     string
	http       *http.Client
	dispatcher *Dispatcher

	// pollTimeout is getUpdates' long-poll timeout; the HTTP client timeout must exceed it.
	pollTimeout int

	// baseURL overrides the Telegram API base for testing; empty = production URL.
	baseURL string
}

// New creates a Listener that only processes messages from chatID (whitespace-trimmed); others are ignored.
func New(botToken, chatID string) *Listener {
	return &Listener{
		botToken:    botToken,
		chatID:      strings.TrimSpace(chatID),
		http:        &http.Client{Timeout: 45 * time.Second},
		dispatcher:  NewDispatcher(),
		pollTimeout: 30,
	}
}

// Dispatcher returns the command dispatcher so callers can register handlers.
func (l *Listener) Dispatcher() *Dispatcher { return l.dispatcher }

// Run polls until ctx is cancelled, tracking the offset to avoid reprocessing and backing off on errors.
func (l *Listener) Run(ctx context.Context) error {
	slog.Info("telegram listener starting", "chat_id", l.chatID)

	offset := 0
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			slog.Info("telegram listener shutting down")
			return nil
		default:
		}

		updates, err := l.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("telegram getUpdates failed, retrying",
				"err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = time.Second

		for _, u := range updates {
			// Advance offset so Telegram doesn't resend this update.
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}

			l.handleUpdate(ctx, u)
		}
	}
}

// handleUpdate dispatches one update; unauthorised senders are silently ignored.
func (l *Listener) handleUpdate(ctx context.Context, u Update) {
	if u.Message == nil {
		return
	}
	msg := u.Message

	// Hard-lock to the configured chat ID.
	senderChatID := fmt.Sprintf("%d", msg.Chat.ID)
	if senderChatID != l.chatID {
		slog.Debug("ignoring message from unauthorised chat",
			"chat_id", senderChatID)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	sender := ""
	if msg.From != nil {
		sender = msg.From.Username
	}
	slog.Info("received message", "from", sender, "text", text)

	reply := l.dispatcher.Dispatch(ctx, text)
	if reply != "" {
		l.sendReply(ctx, reply)
	}
}

// sendReply sends a Markdown reply to the configured chat; errors are logged, not propagated.
func (l *Listener) sendReply(ctx context.Context, text string) {
	base := l.apiBase()
	endpoint := base + "/sendMessage"

	form := url.Values{
		"chat_id":    {l.chatID},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		slog.Warn("failed to build reply request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := l.http.Do(req)
	if err != nil {
		slog.Warn("failed to send reply", "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Warn("reply returned error", "status", resp.StatusCode, "body", string(body))
	}
}

// apiBase returns the Bot API base URL (test override if set).
func (l *Listener) apiBase() string {
	if l.baseURL != "" {
		return l.baseURL
	}
	return "https://api.telegram.org/bot" + l.botToken
}

// getUpdates long-polls the getUpdates endpoint.
func (l *Listener) getUpdates(ctx context.Context, offset int) ([]Update, error) {
	endpoint := fmt.Sprintf("%s/getUpdates?offset=%d&timeout=%d",
		l.apiBase(), offset, l.pollTimeout)
	return l.fetchUpdates(ctx, endpoint)
}

// fetchUpdates does the HTTP GET and JSON decode.
func (l *Listener) fetchUpdates(ctx context.Context, endpoint string) ([]Update, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := l.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s", string(body))
	}

	return result.Result, nil
}

// nextBackoff doubles the duration, capped at 60s.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// --- Telegram Bot API types ------------------------------------------------

// apiResponse is the top-level envelope from getUpdates.
type apiResponse struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

// Update is a single update from the Telegram Bot API.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

// Message is a Telegram message.
type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	Date      int    `json:"date"`
}

// User is a Telegram user.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is a Telegram chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
