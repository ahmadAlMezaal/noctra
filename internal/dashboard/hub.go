package dashboard

import (
	"context"
	"sync"
)

const defaultMaxSubscribers = 64

// Hub fans live dashboard invalidation events out to SSE subscribers.
type Hub struct {
	mu          sync.Mutex
	max         int
	subscribers map[chan struct{}]struct{}
}

// NewHub creates a bounded in-process fan-out hub.
func NewHub(maxSubscribers int) *Hub {
	if maxSubscribers <= 0 {
		maxSubscribers = defaultMaxSubscribers
	}
	return &Hub{
		max:         maxSubscribers,
		subscribers: map[chan struct{}]struct{}{},
	}
}

// Subscribe registers a subscriber until ctx is cancelled or unsubscribe is called.
func (h *Hub) Subscribe(ctx context.Context) (<-chan struct{}, func(), bool) {
	if h == nil {
		ch := make(chan struct{})
		close(ch)
		return ch, func() {}, false
	}
	ch := make(chan struct{}, 1)
	done := make(chan struct{})
	h.mu.Lock()
	if len(h.subscribers) >= h.max {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}, false
	}
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subscribers[ch]; ok {
				delete(h.subscribers, ch)
				close(ch)
			}
			close(done)
			h.mu.Unlock()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			unsubscribe()
		case <-done:
		}
	}()
	return ch, unsubscribe, true
}

// Publish notifies current subscribers without blocking slow clients.
func (h *Hub) Publish() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
