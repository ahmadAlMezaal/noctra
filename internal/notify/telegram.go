// Package notify pushes status messages to Telegram. All sends are fire-
// and-forget — a failure here never blocks the pipeline.
package notify

import (
	"context"
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
		endpoint := "https://api.telegram.org/bot" + t.BotToken + "/sendMessage"
		form := url.Values{
			"chat_id":    {t.ChatID},
			"text":       {message},
			"parse_mode": {"Markdown"},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := t.HTTP.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}
