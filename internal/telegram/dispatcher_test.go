package telegram

import (
	"context"
	"strings"
	"testing"
)

func TestDispatch_KnownCommand(t *testing.T) {
	d := NewDispatcher()
	reply := d.Dispatch(context.Background(), "/ping")
	if reply != "pong" {
		t.Errorf("Dispatch(/ping) = %q, want %q", reply, "pong")
	}
}

func TestDispatch_WithoutSlash(t *testing.T) {
	d := NewDispatcher()
	reply := d.Dispatch(context.Background(), "ping")
	if reply != "pong" {
		t.Errorf("Dispatch(ping) = %q, want %q", reply, "pong")
	}
}

func TestDispatch_CaseInsensitive(t *testing.T) {
	d := NewDispatcher()
	reply := d.Dispatch(context.Background(), "/PING")
	if reply != "pong" {
		t.Errorf("Dispatch(/PING) = %q, want %q", reply, "pong")
	}
}

func TestDispatch_WithArgs(t *testing.T) {
	d := NewDispatcher()
	var gotArgs string
	d.Register("echo", "echo args", func(_ context.Context, args string) string {
		gotArgs = args
		return "ok"
	})

	reply := d.Dispatch(context.Background(), "/echo hello world")
	if reply != "ok" {
		t.Errorf("Dispatch reply = %q, want %q", reply, "ok")
	}
	if gotArgs != "hello world" {
		t.Errorf("handler got args = %q, want %q", gotArgs, "hello world")
	}
}

func TestDispatch_UnknownCommand(t *testing.T) {
	d := NewDispatcher()
	reply := d.Dispatch(context.Background(), "/foobar")
	if !strings.Contains(reply, "Unknown command") {
		t.Errorf("expected unknown command reply, got %q", reply)
	}
	if !strings.Contains(reply, "foobar") {
		t.Errorf("reply should mention the command name, got %q", reply)
	}
}

func TestDispatch_HelpListsCommands(t *testing.T) {
	d := NewDispatcher()
	reply := d.Dispatch(context.Background(), "/help")
	if !strings.Contains(reply, "/ping") {
		t.Errorf("help should list /ping, got %q", reply)
	}
	if !strings.Contains(reply, "/help") {
		t.Errorf("help should list /help, got %q", reply)
	}
}

func TestDispatch_StripsBotMention(t *testing.T) {
	d := NewDispatcher()
	reply := d.Dispatch(context.Background(), "/ping@mybot")
	if reply != "pong" {
		t.Errorf("Dispatch(/ping@mybot) = %q, want %q", reply, "pong")
	}
}

func TestDispatch_RegisterOverwrites(t *testing.T) {
	d := NewDispatcher()
	d.Register("ping", "custom ping", func(_ context.Context, _ string) string {
		return "custom"
	})
	reply := d.Dispatch(context.Background(), "/ping")
	if reply != "custom" {
		t.Errorf("Dispatch(/ping) = %q after overwrite, want %q", reply, "custom")
	}
}
