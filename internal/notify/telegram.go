// Package notify pushes status messages to chat platforms. Sends are fire-and-forget so a failure never blocks processing; SendSync validates credentials in the setup wizard.
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

// New returns a Telegram notifier; Send on a disabled instance no-ops.
func New(enabled bool, botToken, chatID string) *Telegram {
	return &Telegram{
		Enabled:  enabled && botToken != "" && chatID != "",
		BotToken: botToken,
		ChatID:   chatID,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts a Markdown message in a background goroutine and returns immediately; errors are swallowed (best-effort).
func (t *Telegram) Send(ctx context.Context, message string) {
	if t == nil || !t.Enabled {
		return
	}
	go func() {
		// Detach from ctx — the caller may cancel it before the round-trip completes.
		_ = t.post(context.Background(), message)
	}()
}

// SendSync posts synchronously, returning any error; the setup wizard uses it to verify the bot token + chat ID.
func (t *Telegram) SendSync(ctx context.Context, message string) error {
	if t == nil {
		return fmt.Errorf("telegram client is nil")
	}
	if t.BotToken == "" || t.ChatID == "" {
		return fmt.Errorf("missing bot token or chat ID")
	}
	return t.post(ctx, message)
}

// EscapeMarkdown backslash-escapes Telegram's strict legacy-Markdown chars (_ * ` [). Apply to dynamic values (ticket titles, BLOCKED reasons) before interpolating, leaving the template's own *bold* alone — else a title like snake_case_thing returns 400 and the notification is silently dropped (PR #52).
func EscapeMarkdown(s string) string {
	return mdEscaper.Replace(s)
}

var mdEscaper = strings.NewReplacer(
	"_", `\_`,
	"*", `\*`,
	"`", "\\`",
	"[", `\[`,
)

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
