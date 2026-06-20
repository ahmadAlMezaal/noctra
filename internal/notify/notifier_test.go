package notify

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// recorder is a test Notifier that logs calls.
type recorder struct {
	mu       sync.Mutex
	messages []string
	sendErr  error
}

func (r *recorder) Send(ctx context.Context, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
}

func (r *recorder) SendSync(ctx context.Context, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
	return r.sendErr
}

func TestMultiSendFansOut(t *testing.T) {
	a := &recorder{}
	b := &recorder{}
	m := NewMulti([]Notifier{a, b}, []string{"A", "B"})

	m.Send(context.Background(), "hello")

	// Send is async (fires goroutines), so SendSync is a better test.
	if err := m.SendSync(context.Background(), "world"); err != nil {
		t.Fatalf("SendSync: %v", err)
	}

	// SendSync messages are guaranteed to be recorded.
	if len(a.messages) == 0 || a.messages[len(a.messages)-1] != "world" {
		t.Errorf("backend A missing sync message: %v", a.messages)
	}
	if len(b.messages) == 0 || b.messages[len(b.messages)-1] != "world" {
		t.Errorf("backend B missing sync message: %v", b.messages)
	}
}

func TestMultiSendSyncReturnsFirstError(t *testing.T) {
	a := &recorder{sendErr: fmt.Errorf("fail-a")}
	b := &recorder{}
	m := NewMulti([]Notifier{a, b}, []string{"A", "B"})

	err := m.SendSync(context.Background(), "test")
	if err == nil || err.Error() != "fail-a" {
		t.Fatalf("expected fail-a, got %v", err)
	}
	// b should still receive the message.
	if len(b.messages) != 1 {
		t.Errorf("backend B should still receive the message: %v", b.messages)
	}
}

func TestMultiLabels(t *testing.T) {
	m := NewMulti([]Notifier{&recorder{}, &recorder{}}, []string{"Telegram", "Slack"})
	labels := m.Labels()
	if len(labels) != 2 || labels[0] != "Telegram" || labels[1] != "Slack" {
		t.Errorf("Labels() = %v, want [Telegram Slack]", labels)
	}
}

func TestMultiString(t *testing.T) {
	cases := []struct {
		name    string
		multi   *Multi
		want    string
	}{
		{"nil", nil, "Disabled"},
		{"empty", NewMulti(nil, nil), "Disabled"},
		{"one", NewMulti([]Notifier{&recorder{}}, []string{"Telegram"}), "Telegram"},
		{"two", NewMulti([]Notifier{&recorder{}, &recorder{}}, []string{"Slack", "Discord"}), "Slack, Discord"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.multi.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMultiNilSafe(t *testing.T) {
	var m *Multi
	// Should not panic.
	m.Send(context.Background(), "test")
	if m.String() != "Disabled" {
		t.Errorf("nil Multi.String() should be Disabled")
	}
	if m.Labels() != nil {
		t.Errorf("nil Multi.Labels() should be nil")
	}
}

func TestMultiFiltersNilBackends(t *testing.T) {
	m := NewMulti([]Notifier{nil, &recorder{}, nil}, []string{"X", "Real", "Y"})
	if len(m.backends) != 1 {
		t.Errorf("expected 1 backend after filtering nils, got %d", len(m.backends))
	}
	if m.String() != "Real" {
		t.Errorf("String() = %q, want %q", m.String(), "Real")
	}
}

// Verify concrete types satisfy the Notifier interface at compile time.
var (
	_ Notifier = (*Telegram)(nil)
	_ Notifier = (*Slack)(nil)
	_ Notifier = (*Discord)(nil)
)
