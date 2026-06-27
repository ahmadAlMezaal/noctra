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

// NewSlack returns a Slack notifier; a non-empty webhook URL enables it, an empty one no-ops.
func NewSlack(webhookURL string) *Slack {
	return &Slack{
		Enabled:    webhookURL != "",
		WebhookURL: webhookURL,
		HTTP:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts in a background goroutine and returns immediately; errors are swallowed (best-effort).
func (s *Slack) Send(ctx context.Context, message string) {
	if s == nil || !s.Enabled {
		return
	}
	go func() {
		// Detach from ctx — the caller may cancel it before the round-trip completes.
		_ = s.post(context.Background(), message)
	}()
}

// SendSync posts synchronously, returning any error; the setup wizard uses it to verify the webhook URL.
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
