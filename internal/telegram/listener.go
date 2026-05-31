// Package telegram implements a long-polling listener for inbound Telegram
// messages. It authenticates the sender against the configured chat ID,
// parses commands, and routes them to registered handlers.
//
// This is the inbound counterpart to internal/notify (outbound). Both use the
// same TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID from .env — same bot, now
// bidirectional. Long-polling via getUpdates is the right fit for a Pi behind
// a home network: no inbound ports, no tunnel, no TLS to manage.
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

	// pollTimeout is the long-poll timeout sent to Telegram's getUpdates.
	// Telegram holds the connection open for this duration, then returns an
	// empty result if no updates arrived. Separate from the HTTP client
	// timeout (which must be longer to avoid premature cancellation).
	pollTimeout int

	// baseURL overrides the Telegram API base for testing. When empty, the
	// production URL (https://api.telegram.org/bot<token>) is used.
	baseURL string
}

// New creates a Listener. The chatID is used for sender authorisation — only
// messages from this chat are processed; everything else is silently ignored.
// Leading/trailing whitespace is trimmed from chatID for robustness.
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

// Run blocks, polling Telegram for updates until ctx is cancelled. It
// tracks the update offset so messages are never reprocessed, and retries
// with exponential backoff on transient errors.
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

		// Reset backoff on success.
		backoff = time.Second

		for _, u := range updates {
			// Advance offset past this update so Telegram doesn't resend it.
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}

			l.handleUpdate(ctx, u)
		}
	}
}

// handleUpdate processes a single update. Unauthorised senders are silently
// ignored. Recognised commands are dispatched; unknown ones get a helpful reply.
func (l *Listener) handleUpdate(ctx context.Context, u Update) {
	if u.Message == nil {
		return
	}
	msg := u.Message

	// Sender authorisation: hard-lock to the configured chat ID.
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

// sendReply sends a Markdown-formatted reply to the configured chat. Best-
// effort — errors are logged but don't interrupt the poll loop.
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

// apiBase returns the Bot API base URL, using the test override if set.
func (l *Listener) apiBase() string {
	if l.baseURL != "" {
		return l.baseURL
	}
	return "https://api.telegram.org/bot" + l.botToken
}

// getUpdates calls the Telegram Bot API getUpdates endpoint with long-polling.
func (l *Listener) getUpdates(ctx context.Context, offset int) ([]Update, error) {
	endpoint := fmt.Sprintf("%s/getUpdates?offset=%d&timeout=%d",
		l.apiBase(), offset, l.pollTimeout)
	return l.fetchUpdates(ctx, endpoint)
}

// fetchUpdates does the actual HTTP GET and JSON decode.
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

// nextBackoff doubles the backoff duration, capping at 60 seconds.
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
