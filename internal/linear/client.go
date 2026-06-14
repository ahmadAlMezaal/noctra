package linear

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

const defaultEndpoint = "https://api.linear.app/graphql"

// Client is a tiny Linear GraphQL client.
type Client struct {
	APIKey   string
	Endpoint string
	HTTP     *http.Client
	// Bearer controls how APIKey is sent: personal API keys go in the
	// Authorization header verbatim, OAuth access tokens are prefixed "Bearer ".
	Bearer bool
}

// New constructs a Client authenticated with a personal API key.
func New(apiKey string) *Client {
	return &Client{
		APIKey:   apiKey,
		Endpoint: defaultEndpoint,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// NewOAuth constructs a Client authenticated with an OAuth access token. When
// that token was issued with `actor=app`, Noctra's comments and state changes
// are attributed to the application identity rather than the authorizing user.
func NewOAuth(token string) *Client {
	c := New(token)
	c.Bearer = true
	return c
}

// Do runs a GraphQL operation. On success the contents of "data" are decoded
// into out (if non-nil). Linear's `errors` array is surfaced as a Go error.
func (c *Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	auth := c.APIKey
	if c.Bearer {
		auth = "Bearer " + c.APIKey
	}
	req.Header.Set("Authorization", auth)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("linear request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(body), 256))
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
	}

	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
