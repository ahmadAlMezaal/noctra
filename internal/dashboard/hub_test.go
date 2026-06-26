package dashboard

import (
	"context"
	"testing"
	"time"
)

func TestHubPublishAndUnsubscribe(t *testing.T) {
	h := NewHub(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, unsubA, ok := h.Subscribe(ctx)
	if !ok {
		t.Fatal("subscribe A rejected")
	}
	b, unsubB, ok := h.Subscribe(ctx)
	if !ok {
		t.Fatal("subscribe B rejected")
	}
	defer unsubB()

	h.Publish()
	receive(t, a)
	receive(t, b)

	unsubA()
	h.Publish()
	receive(t, b)
	select {
	case _, ok := <-a:
		if ok {
			t.Fatal("unsubscribed channel received publish")
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubSubscriberCap(t *testing.T) {
	h := NewHub(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, unsub, ok := h.Subscribe(ctx)
	if !ok {
		t.Fatal("first subscriber rejected")
	}
	defer unsub()
	_, _, ok = h.Subscribe(ctx)
	if ok {
		t.Fatal("second subscriber accepted over cap")
	}
}

func receive(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for publish")
	}
}
