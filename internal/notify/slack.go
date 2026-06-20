package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Slack sends notifications via an incoming webhook URL.
type Slack struct {
	Enabled    bool
	WebhookURL string
	HTTP       *http.Client
}

// NewSlack returns a Slack notifier. A non-empty webhook URL enables it;
// an empty URL yields a disabled notifier whose Send is a safe no-op.
func NewSlack(webhookURL string) *Slack {
	return &Slack{
		Enabled:    webhookURL != "",
		WebhookURL: webhookURL,
		HTTP:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts a message in a background goroutine and returns immediately.
// Errors are intentionally swallowed — notifications are best-effort.
func (s *Slack) Send(ctx context.Context, message string) {
	if s == nil || !s.Enabled {
		return
	}
	go func() {
		// Detach from the caller's context — the caller may return (and
		// cancel ctx) before the HTTP round-trip completes.
		_ = s.post(context.Background(), message)
	}()
}

// SendSync posts a message synchronously and returns any error. The setup
// wizard uses this to verify a webhook URL actually works before saving.
func (s *Slack) SendSync(ctx context.Context, message string) error {
	if s == nil {
		return fmt.Errorf("slack client is nil")
	}
	if s.WebhookURL == "" {
		return fmt.Errorf("missing webhook URL")
	}
	return s.post(ctx, message)
}

func (s *Slack) post(ctx context.Context, message string) error {
	if s.HTTP == nil {
		return fmt.Errorf("slack HTTP client is nil")
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: message})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("slack returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
