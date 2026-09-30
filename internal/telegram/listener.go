package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/onelastcommit/noctra/internal/notify"
)

type Listener struct {
	botToken   string
	chatID     string
	http       *http.Client
	dispatcher *Dispatcher

	pollTimeout int

	baseURL string
}

func New(botToken, chatID string) *Listener {
	return &Listener{
		botToken:    botToken,
		chatID:      strings.TrimSpace(chatID),
		http:        &http.Client{Timeout: 45 * time.Second},
		dispatcher:  NewDispatcher(),
		pollTimeout: 30,
	}
}

func (l *Listener) Dispatcher() *Dispatcher { return l.dispatcher }

func (l *Listener) Run(ctx context.Context) error {
	slog.Info("telegram listener starting", "chat_id", l.chatID)

	offset := 0
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			slog.Info("telegram listener shutting down")
			return nil
		default:
		}

		updates, err := l.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("telegram getUpdates failed, retrying",
				"err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = time.Second

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}

			l.handleUpdate(ctx, u)
		}
	}
}

func (l *Listener) handleUpdate(ctx context.Context, u Update) {
	if u.Message == nil {
		return
	}
	msg := u.Message

	senderChatID := fmt.Sprintf("%d", msg.Chat.ID)
	if senderChatID != l.chatID {
		slog.Debug("ignoring message from unauthorised chat",
			"chat_id", senderChatID)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	sender := ""
	if msg.From != nil {
		sender = msg.From.Username
	}
	slog.Info("received message", "from", sender, "text", text)

	reply := l.dispatcher.Dispatch(ctx, text)
	if reply != "" {
		l.sendReply(ctx, reply)
	}
}

func (l *Listener) sendReply(ctx context.Context, text string) {
	base := l.apiBase()
	endpoint := base + "/sendMessage"

	form := url.Values{
		"chat_id":    {l.chatID},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		slog.Warn("failed to build reply request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := l.http.Do(req)
	if err != nil {
		slog.Warn("failed to send reply", "err", notify.RedactURLError(err, l.botToken))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Warn("reply returned error", "status", resp.StatusCode, "body", string(body))
	}
}

func (l *Listener) apiBase() string {
	if l.baseURL != "" {
		return l.baseURL
	}
	return "https://api.telegram.org/bot" + l.botToken
}

func (l *Listener) getUpdates(ctx context.Context, offset int) ([]Update, error) {
	endpoint := fmt.Sprintf("%s/getUpdates?offset=%d&timeout=%d",
		l.apiBase(), offset, l.pollTimeout)
	return l.fetchUpdates(ctx, endpoint)
}

func (l *Listener) fetchUpdates(ctx context.Context, endpoint string) ([]Update, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := l.http.Do(req)
	if err != nil {
		return nil, notify.RedactURLError(err, l.botToken)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s", string(body))
	}

	return result.Result, nil
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

type apiResponse struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	Date      int    `json:"date"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
