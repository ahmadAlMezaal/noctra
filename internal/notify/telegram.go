package notify

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Telegram struct {
	Enabled  bool
	BotToken string
	ChatID   string
	HTTP     *http.Client
}

func New(enabled bool, botToken, chatID string) *Telegram {
	return &Telegram{
		Enabled:  enabled && botToken != "" && chatID != "",
		BotToken: botToken,
		ChatID:   chatID,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *Telegram) Send(ctx context.Context, message string) {
	if t == nil || !t.Enabled {
		return
	}
	go func() {
		if err := t.post(context.Background(), message); err != nil {
			slog.Warn("notify: telegram send failed", "err", err)
		}
	}()
}

func (t *Telegram) SendSync(ctx context.Context, message string) error {
	if t == nil {
		return fmt.Errorf("telegram client is nil")
	}
	if t.BotToken == "" || t.ChatID == "" {
		return fmt.Errorf("missing bot token or chat ID")
	}
	return t.post(ctx, message)
}

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
		return RedactURLError(err, t.BotToken)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func RedactURLError(err error, secret string) error {
	urlErr, ok := err.(*url.Error)
	if !ok || secret == "" {
		return err
	}
	return &url.Error{
		Op:  urlErr.Op,
		URL: strings.ReplaceAll(urlErr.URL, secret, "<redacted>"),
		Err: urlErr.Err,
	}
}
