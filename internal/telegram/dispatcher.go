package telegram

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ahmadAlMezaal/noctra/internal/notify"
)

// HandlerFunc processes a command and returns a reply message. An empty reply
// means no message is sent back.
type HandlerFunc func(ctx context.Context, args string) string

// Dispatcher routes incoming text messages to registered command handlers.
type Dispatcher struct {
	commands map[string]command
}

type command struct {
	handler     HandlerFunc
	description string
}

// NewDispatcher creates a Dispatcher with the built-in /help and /ping
// commands pre-registered.
func NewDispatcher() *Dispatcher {
	d := &Dispatcher{
		commands: make(map[string]command),
	}
	d.Register("help", "List available commands", d.helpHandler)
	d.Register("ping", "Check if the listener is alive", pingHandler)
	return d
}

// Register adds a command handler. The name should not include the leading
// slash — it's stripped during dispatch. Registering the same name twice
// overwrites the previous handler. Leading slashes and surrounding whitespace
// are stripped from name for robustness.
func (d *Dispatcher) Register(name, description string, handler HandlerFunc) {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	d.commands[strings.ToLower(name)] = command{
		handler:     handler,
		description: description,
	}
}

// Dispatch parses the message text, extracts the command keyword and args,
// and routes to the matching handler. Returns the reply text (empty means
// no reply).
func (d *Dispatcher) Dispatch(ctx context.Context, text string) string {
	// Strip leading slash if present (Telegram sends "/status" for commands).
	text = strings.TrimPrefix(text, "/")

	// Split into command + args.
	parts := strings.SplitN(text, " ", 2)
	name := strings.ToLower(parts[0])
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	// Strip @botname suffix that Telegram appends in groups (e.g. "/ping@mybot").
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}

	cmd, ok := d.commands[name]
	if !ok {
		return d.unknownReply(name)
	}
	return cmd.handler(ctx, args)
}

// unknownReply returns a helpful message listing available commands.
func (d *Dispatcher) unknownReply(name string) string {
	return fmt.Sprintf("Unknown command: *%s*\n\nType /help to see available commands.",
		notify.EscapeMarkdown(name))
}

func (d *Dispatcher) helpHandler(_ context.Context, _ string) string {
	names := make([]string, 0, len(d.commands))
	for name := range d.commands {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("*Noctra Commands*\n\n")
	for _, name := range names {
		cmd := d.commands[name]
		fmt.Fprintf(&b, "/%s — %s\n",
			notify.EscapeMarkdown(name),
			notify.EscapeMarkdown(cmd.description))
	}
	return b.String()
}

func pingHandler(_ context.Context, _ string) string {
	return "pong"
}
