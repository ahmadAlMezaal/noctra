// Package notify pushes status messages to Telegram. Pipeline sends are fire-
// and-forget — a failure here never blocks ticket processing. The setup
// wizard uses SendSync to validate credentials before they're saved.
package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Telegram is a tiny Bot-API client.
type Telegram struct {
	Enabled  bool
	BotToken string
	ChatID   string
	HTTP     *http.Client
}

// New returns a Telegram notifier. It's safe to call Send on a disabled
// instance — it just no-ops.
func New(enabled bool, botToken, chatID string) *Telegram {
	return &Telegram{
		Enabled:  enabled && botToken != "" && chatID != "",
		BotToken: botToken,
		ChatID:   chatID,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts a Markdown message in a background goroutine and returns
// immediately. Errors are intentionally swallowed — notifications are
// best-effort and shouldn't gate ticket processing.
func (t *Telegram) Send(ctx context.Context, message string) {
	if t == nil || !t.Enabled {
		return
	}
	go func() {
		_ = t.post(ctx, message)
	}()
}

// SendSync posts a Markdown message synchronously and returns any error. The
// setup wizard uses this to verify a bot token + chat ID actually work before
// writing them to .env.
func (t *Telegram) SendSync(ctx context.Context, message string) error {
	if t == nil {
		return fmt.Errorf("telegram client is nil")
	}
	if t.BotToken == "" || t.ChatID == "" {
		return fmt.Errorf("missing bot token or chat ID")
	}
	return t.post(ctx, message)
}

func (t *Telegram) post(ctx context.Context, message string) error {
	endpoint := "https://api.telegram.org/bot" + t.BotToken + "/sendMessage"
	form := url.Values{
		"chat_id":    {t.ChatID},
		"text":       {message},
		"parse_mode": {"Markdown"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
