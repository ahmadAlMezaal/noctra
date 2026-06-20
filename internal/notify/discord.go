package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// maxDiscordLen is Discord's per-message character limit. Messages longer
// than this are truncated with an ellipsis to avoid 400 errors.
const maxDiscordLen = 2000

// discordBoldRe matches Telegram-style single-asterisk bold (*text*).
// Noctra's messages are templated with Telegram/Slack mrkdwn, where a
// single * is bold. Discord treats single * as italic and uses ** for
// bold, so we rewrite *text* → **text** to keep emphasis consistent
// across platforms. Templates only ever use single *, so this won't
// double-wrap existing **.
var discordBoldRe = regexp.MustCompile(`\*([^*\n]+)\*`)

// toDiscordMarkdown converts single-asterisk bold to Discord's double-
// asterisk bold so status messages render bold rather than italic.
func toDiscordMarkdown(s string) string {
	return discordBoldRe.ReplaceAllString(s, "**$1**")
}

// Discord sends notifications via a Discord webhook URL.
type Discord struct {
	Enabled    bool
	WebhookURL string
	HTTP       *http.Client
}

// NewDiscord returns a Discord notifier. A non-empty webhook URL enables
// it; an empty URL yields a disabled notifier whose Send is a safe no-op.
func NewDiscord(webhookURL string) *Discord {
	return &Discord{
		Enabled:    webhookURL != "",
		WebhookURL: webhookURL,
		HTTP:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts a message in a background goroutine and returns immediately.
// Errors are intentionally swallowed — notifications are best-effort.
func (d *Discord) Send(ctx context.Context, message string) {
	if d == nil || !d.Enabled {
		return
	}
	go func() {
		// Detach from the caller's context — the caller may return (and
		// cancel ctx) before the HTTP round-trip completes.
		_ = d.post(context.Background(), message)
	}()
}

// SendSync posts a message synchronously and returns any error. The setup
// wizard uses this to verify a webhook URL actually works before saving.
func (d *Discord) SendSync(ctx context.Context, message string) error {
	if d == nil {
		return fmt.Errorf("discord client is nil")
	}
	if d.WebhookURL == "" {
		return fmt.Errorf("missing webhook URL")
	}
	return d.post(ctx, message)
}

func (d *Discord) post(ctx context.Context, message string) error {
	if d.HTTP == nil {
		return fmt.Errorf("discord HTTP client is nil")
	}
	// Rewrite single-asterisk bold to Discord's ** before truncation so the
	// rendered text matches Telegram/Slack emphasis.
	message = toDiscordMarkdown(message)

	// Truncate by runes, not bytes, to avoid splitting multi-byte UTF-8.
	runes := []rune(message)
	if len(runes) > maxDiscordLen {
		message = string(runes[:maxDiscordLen-3]) + "..."
	}

	// allowed_mentions with an empty parse list disables ALL mention parsing
	// (@everyone, @here, role/user pings). Status text can include untrusted
	// Linear ticket titles/comments, so without this a crafted ticket could
	// mass-ping a server. The parse slice must marshal to [] (not null), so
	// it's initialized to a non-nil empty slice.
	body := struct {
		Content         string `json:"content"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}{Content: message}
	body.AllowedMentions.Parse = []string{}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Discord returns 204 No Content on success.
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
