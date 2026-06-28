package notify

import (
	"context"
	"fmt"
	"strings"
)

// Notifier sends status messages to a chat platform. Concurrency- and nil-safe (a disabled notifier no-ops).
type Notifier interface {
	// Send posts asynchronously (fire-and-forget); errors are swallowed so notifications never block processing.
	Send(ctx context.Context, message string)

	// SendSync posts synchronously, returning any error. Used by the setup wizard to verify credentials.
	SendSync(ctx context.Context, message string) error
}

// Multi fans out notifications to zero or more backends; all methods are nil-safe.
type Multi struct {
	backends []Notifier
	labels   []string
}

// NewMulti returns a Multi fanning out to the given backends, each paired with a label (nil backends ignored).
func NewMulti(backends []Notifier, labels []string) *Multi {
	var (
		filtered       []Notifier
		filteredLabels []string
	)
	for i, b := range backends {
		if b == nil {
			continue
		}
		filtered = append(filtered, b)
		if i < len(labels) {
			filteredLabels = append(filteredLabels, labels[i])
		}
	}
	return &Multi{backends: filtered, labels: filteredLabels}
}

// Send fans out to every backend's Send (fire-and-forget).
func (m *Multi) Send(ctx context.Context, message string) {
	if m == nil {
		return
	}
	for _, b := range m.backends {
		b.Send(ctx, message)
	}
}

// SendSync fans out to every backend's SendSync, returning the first error (later backends still run).
func (m *Multi) SendSync(ctx context.Context, message string) error {
	if m == nil {
		return fmt.Errorf("notifier is nil")
	}
	var firstErr error
	for _, b := range m.backends {
		if err := b.SendSync(ctx, message); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Labels returns the display labels for every active backend, for the startup banner.
func (m *Multi) Labels() []string {
	if m == nil {
		return nil
	}
	return m.labels
}

// String returns a comma-separated summary of active backends, or "Disabled" if none.
func (m *Multi) String() string {
	if m == nil || len(m.labels) == 0 {
		return "Disabled"
	}
	return strings.Join(m.labels, ", ")
}
