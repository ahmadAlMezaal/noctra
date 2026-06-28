package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const defaultEndpoint = "https://api.linear.app/graphql"

// Client is a tiny Linear GraphQL client.
type Client struct {
	APIKey   string
	Endpoint string
	HTTP     *http.Client
	// Bearer sends APIKey with a "Bearer " prefix (OAuth) vs verbatim (personal key).
	Bearer bool

	// TokenFn supplies a fresh bearer token per request (auto-refreshing actor=app OAuth); takes precedence over APIKey/Bearer.
	TokenFn func(ctx context.Context) (string, error)
	// OnAuthError forces one credential refresh after an auth failure, then the request retries once.
	OnAuthError func(ctx context.Context) error
	// FallbackAPIKey is the personal key degraded to when OAuth keeps failing, so an expired app token can't crash-loop.
	FallbackAPIKey string
	// OnDegrade fires once on first fallback (e.g. to alert).
	OnDegrade func(cause error)

	degraded atomic.Bool
}

// New constructs a Client authenticated with a personal API key.
func New(apiKey string) *Client {
	return &Client{
		APIKey:   apiKey,
		Endpoint: defaultEndpoint,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// NewOAuth constructs a Client using an OAuth access token; an actor=app token attributes actions to the app identity.
func NewOAuth(token string) *Client {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "Bearer ")
	c := New(token)
	c.Bearer = true
	return c
}

// Do runs a GraphQL op, decoding "data" into out; on auth failure it refreshes+retries once, then degrades to FallbackAPIKey.
func (c *Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	if c.FallbackAPIKey != "" && c.degraded.Load() {
		return c.exec(ctx, c.FallbackAPIKey, query, vars, out)
	}

	authz, err := c.authHeader(ctx)
	if err != nil {
		if c.FallbackAPIKey != "" {
			c.degradeOnce(err)
			return c.exec(ctx, c.FallbackAPIKey, query, vars, out)
		}
		return err
	}

	execErr := c.exec(ctx, authz, query, vars, out)
	if execErr == nil || !isAuthError(execErr) {
		return execErr
	}

	if c.OnAuthError != nil {
		if rerr := c.OnAuthError(ctx); rerr != nil {
			execErr = rerr // surface refresh failure as the degrade cause
		} else if authz2, aerr := c.authHeader(ctx); aerr != nil {
			execErr = aerr
		} else if e2 := c.exec(ctx, authz2, query, vars, out); e2 == nil || !isAuthError(e2) {
			return e2
		} else {
			execErr = e2
		}
	}

	if c.FallbackAPIKey != "" {
		c.degradeOnce(execErr)
		return c.exec(ctx, c.FallbackAPIKey, query, vars, out)
	}
	return execErr
}

// authHeader builds the Authorization header for the primary credential.
func (c *Client) authHeader(ctx context.Context) (string, error) {
	if c.TokenFn != nil {
		tok, err := c.TokenFn(ctx)
		if err != nil {
			return "", err
		}
		return "Bearer " + tok, nil
	}
	if c.Bearer {
		return "Bearer " + c.APIKey, nil
	}
	return c.APIKey, nil
}

// degradeOnce latches the fallback and fires OnDegrade exactly once.
func (c *Client) degradeOnce(cause error) {
	if c.degraded.CompareAndSwap(false, true) {
		slog.Warn("linear: app identity (OAuth) failed to authenticate; falling back to personal API key", "err", cause)
		if c.OnDegrade != nil {
			c.OnDegrade(cause)
		}
	}
}

// isAuthError reports whether err looks like a Linear auth rejection.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "authentication") ||
		strings.Contains(s, "not authenticated") ||
		strings.Contains(s, "unauthorized")
}

// exec performs a single GraphQL request with the given Authorization header.
func (c *Client) exec(ctx context.Context, authz, query string, vars map[string]any, out any) error {
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
	req.Header.Set("Authorization", authz)

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
