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

// maxDiscordLen is Discord's per-message char limit; longer messages are ellipsis-truncated to avoid 400s.
const maxDiscordLen = 2000

// discordBoldRe matches Telegram/Slack single-* bold; Discord uses ** for bold (single * is italic). Templates only use single *, so no double-wrapping.
var discordBoldRe = regexp.MustCompile(`\*([^*\n]+)\*`)

// toDiscordMarkdown rewrites single-* bold to Discord's ** bold so emphasis renders bold not italic.
func toDiscordMarkdown(s string) string {
	return discordBoldRe.ReplaceAllString(s, "**$1**")
}

// Discord sends notifications via a Discord webhook URL.
type Discord struct {
	Enabled    bool
	WebhookURL string
	HTTP       *http.Client
}

// NewDiscord returns a Discord notifier; a non-empty webhook URL enables it, an empty one no-ops.
func NewDiscord(webhookURL string) *Discord {
	return &Discord{
		Enabled:    webhookURL != "",
		WebhookURL: webhookURL,
		HTTP:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts in a background goroutine and returns immediately; errors are swallowed (best-effort).
func (d *Discord) Send(ctx context.Context, message string) {
	if d == nil || !d.Enabled {
		return
	}
	go func() {
		// Detach from ctx — the caller may cancel it before the round-trip completes.
		_ = d.post(context.Background(), message)
	}()
}

// SendSync posts synchronously, returning any error; the setup wizard uses it to verify the webhook URL.
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
	// Rewrite bold before truncation so rendered text matches Telegram/Slack emphasis.
	message = toDiscordMarkdown(message)

	// Truncate by runes not bytes to avoid splitting multi-byte UTF-8.
	runes := []rune(message)
	if len(runes) > maxDiscordLen {
		message = string(runes[:maxDiscordLen-3]) + "..."
	}

	// Empty parse list disables ALL mention parsing (@everyone/@here/pings) — untrusted ticket text must not mass-ping. Must be a non-nil slice so it marshals to [] not null.
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

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
