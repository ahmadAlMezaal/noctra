package telegram

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onelastcommit/noctra/internal/notify"
)

const sessionTTL = 5 * time.Minute

const cancelCommand = "cancel"

type HandlerFunc func(ctx context.Context, args string) string

type Conversation interface {
	Answer(ctx context.Context, text string) (reply string, done bool)
}

type StartFunc func(ctx context.Context, args string) (prompt string, conv Conversation)

type Dispatcher struct {
	mu       sync.Mutex
	commands map[string]command
	pending  *session

	now func() time.Time
}

type command struct {
	handler     HandlerFunc
	start       StartFunc
	description string
}

type session struct {
	command string
	conv    Conversation
	expires time.Time
}

func NewDispatcher() *Dispatcher {
	d := &Dispatcher{
		commands: make(map[string]command),
	}
	d.Register("help", "List available commands", d.helpHandler)
	d.Register("ping", "Check if the listener is alive", pingHandler)
	d.Register(cancelCommand, "Abandon the current guided flow", d.cancelHandler)
	return d
}

func (d *Dispatcher) Register(name, description string, handler HandlerFunc) {
	d.register(name, command{handler: handler, description: description})
}

func (d *Dispatcher) RegisterConversation(name, description string, start StartFunc) {
	d.register(name, command{start: start, description: description})
}

func (d *Dispatcher) register(name string, cmd command) {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")

	d.mu.Lock()
	defer d.mu.Unlock()
	d.commands[strings.ToLower(name)] = cmd
}

func (d *Dispatcher) Dispatch(ctx context.Context, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	if !strings.HasPrefix(text, "/") {
		if reply, handled := d.answerPending(ctx, text); handled {
			return reply
		}
	}

	name, args := splitCommand(text)

	interrupted := ""
	if name != cancelCommand {
		interrupted = d.cancelPending()
	}

	d.mu.Lock()
	cmd, ok := d.commands[name]
	d.mu.Unlock()

	if !ok {
		return withInterrupted(interrupted, d.unknownReply(name))
	}
	if cmd.start != nil {
		prompt, conv := cmd.start(ctx, args)
		if conv != nil {
			d.begin(name, conv)
		}
		return withInterrupted(interrupted, prompt)
	}
	return withInterrupted(interrupted, cmd.handler(ctx, args))
}

func (d *Dispatcher) answerPending(ctx context.Context, text string) (string, bool) {
	d.mu.Lock()
	s := d.pending
	if s == nil {
		d.mu.Unlock()
		return "", false
	}
	if d.clock().After(s.expires) {
		d.pending = nil
		d.mu.Unlock()
		return fmt.Sprintf("⌛ The /%s flow timed out. Start it again when you're ready.", s.command), true
	}
	d.mu.Unlock()

	reply, done := s.conv.Answer(ctx, text)

	d.mu.Lock()
	if d.pending == s {
		if done {
			d.pending = nil
		} else {
			s.expires = d.clock().Add(sessionTTL)
		}
	}
	d.mu.Unlock()
	return reply, true
}

func (d *Dispatcher) begin(name string, conv Conversation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = &session{command: name, conv: conv, expires: d.clock().Add(sessionTTL)}
}

func (d *Dispatcher) cancelPending() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending == nil {
		return ""
	}
	name := d.pending.command
	d.pending = nil
	return name
}

func (d *Dispatcher) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func withInterrupted(cancelled, reply string) string {
	if cancelled == "" {
		return reply
	}
	return fmt.Sprintf("✖ Abandoned the /%s flow.\n\n%s", cancelled, reply)
}

func splitCommand(text string) (name, args string) {
	text = strings.TrimPrefix(text, "/")

	parts := strings.SplitN(text, " ", 2)
	name = strings.ToLower(parts[0])
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}
	return name, args
}

func (d *Dispatcher) unknownReply(name string) string {
	return fmt.Sprintf("Unknown command: *%s*\n\nType /help to see available commands.",
		notify.EscapeMarkdown(name))
}

func (d *Dispatcher) cancelHandler(_ context.Context, _ string) string {
	if name := d.cancelPending(); name != "" {
		return fmt.Sprintf("✖ Cancelled the /%s flow.", name)
	}
	return "Nothing to cancel."
}

func (d *Dispatcher) helpHandler(_ context.Context, _ string) string {
	d.mu.Lock()
	names := make([]string, 0, len(d.commands))
	for name := range d.commands {
		names = append(names, name)
	}
	descriptions := make(map[string]string, len(d.commands))
	for name, cmd := range d.commands {
		descriptions[name] = cmd.description
	}
	d.mu.Unlock()

	sort.Strings(names)

	var b strings.Builder
	b.WriteString("*Noctra Commands*\n\n")
	for _, name := range names {
		fmt.Fprintf(&b, "/%s — %s\n",
			notify.EscapeMarkdown(name),
			notify.EscapeMarkdown(descriptions[name]))
	}
	return b.String()
}

func pingHandler(_ context.Context, _ string) string {
	return "pong"
}
