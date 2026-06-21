package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ahmadAlMezaal/noctra/internal/config"
	"github.com/ahmadAlMezaal/noctra/internal/linear"
	"github.com/ahmadAlMezaal/noctra/internal/repo"
	"github.com/ahmadAlMezaal/noctra/internal/source"
)

func TestSkipPermanently_AddToSetAndRefundsDispatch(t *testing.T) {
	p := &Pipeline{
		skipped:        map[string]struct{}{},
		active:         map[string]struct{}{},
		failedAttempts: map[string]int{},
	}
	p.totalDispatches = 5

	p.skipPermanently("ENG-100")

	if _, ok := p.skipped["ENG-100"]; !ok {
		t.Fatal("ENG-100 should be in the skipped set")
	}
	if p.totalDispatches != 4 {
		t.Fatalf("totalDispatches: got %d, want 4 (should be refunded)", p.totalDispatches)
	}

	// Skipping again should be idempotent and not decrement again.
	p.skipPermanently("ENG-100")
	if p.totalDispatches != 4 {
		t.Fatalf("totalDispatches: got %d after second skip, want 4 (should be idempotent)", p.totalDispatches)
	}
}

func TestNonTransientError_Detectable(t *testing.T) {
	inner := errors.New("no repo is mapped for project \"Foo\"")
	nte := &repo.NonTransientError{Err: inner}

	// errors.As should find it.
	var target *repo.NonTransientError
	if !errors.As(nte, &target) {
		t.Fatal("errors.As should detect NonTransientError")
	}
	if target.Error() != inner.Error() {
		t.Errorf("Error(): got %q, want %q", target.Error(), inner.Error())
	}

	// Unwrap should return the inner error.
	if errors.Unwrap(nte) != inner {
		t.Fatal("Unwrap should return the inner error")
	}

	// A plain error should NOT match.
	plain := errors.New("transient clone failure")
	if errors.As(plain, &target) {
		t.Fatal("plain error should not match NonTransientError")
	}
}

func TestBumpFailed_BoundsRetries(t *testing.T) {
	p := &Pipeline{
		skipped:        map[string]struct{}{},
		active:         map[string]struct{}{},
		failedAttempts: map[string]int{},
	}

	for i := 1; i <= 5; i++ {
		got := p.bumpFailed("ENG-200")
		if got != i {
			t.Fatalf("bumpFailed attempt %d: got %d", i, got)
		}
	}
	if p.failCount != 5 {
		t.Fatalf("failCount: got %d, want 5", p.failCount)
	}
}

func TestPollOnce_OperatorPauseSkipsFetch(t *testing.T) {
	var fetched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"teams": map[string]any{"nodes": []map[string]any{}},
			},
		})
	}))
	defer srv.Close()

	client := linear.New("test-key")
	client.Endpoint = srv.URL

	p := &Pipeline{
		cfg: &config.Config{
			MaxConcurrent: 1,
			MaxDispatches: 10,
			TriggerMode:   "state",
			TriggerState:  "Next",
		},
		linear:         client,
		active:         map[string]struct{}{},
		cancels:        map[string]context.CancelFunc{},
		failedAttempts: map[string]int{},
		skipped:        map[string]struct{}{},
		sessionStart:   time.Now(),
		paused:         true,
	}

	var wg sync.WaitGroup
	p.pollOnce(context.Background(), &wg)
	wg.Wait()

	if fetched {
		t.Fatal("pollOnce fetched tickets while operator pause was active")
	}
}

func TestFetchTickets_ContinuesAfterOneSourceFails(t *testing.T) {
	p := &Pipeline{
		sources: []source.TicketSource{
			fetchSource{name: "broken", err: errors.New("boom")},
			fetchSource{name: "ok", tickets: []source.Ticket{{
				Source:     "ok",
				ID:         "ok-1",
				Identifier: "OK-1",
				Title:      "Keep polling",
			}}},
		},
	}

	tickets, err := p.fetchTickets(context.Background())
	if err != nil {
		t.Fatalf("fetchTickets returned error for partial source failure: %v", err)
	}
	if len(tickets) != 1 || tickets[0].Identifier != "OK-1" {
		t.Fatalf("tickets = %#v; want OK-1", tickets)
	}
}

func TestFetchTickets_ErrorsWhenAllSourcesFail(t *testing.T) {
	p := &Pipeline{
		sources: []source.TicketSource{
			fetchSource{name: "broken-a", err: errors.New("boom")},
			fetchSource{name: "broken-b", err: errors.New("bang")},
		},
	}

	if _, err := p.fetchTickets(context.Background()); err == nil {
		t.Fatal("fetchTickets returned nil error when every source failed")
	}
}

type fetchSource struct {
	name    string
	tickets []source.Ticket
	err     error
}

func (s fetchSource) Name() string { return s.name }

func (s fetchSource) Prepare(context.Context) error { return nil }

func (s fetchSource) Fetch(context.Context) ([]source.Ticket, error) {
	return s.tickets, s.err
}

func (s fetchSource) FetchByIdentifier(context.Context, string) (source.Ticket, error) {
	return source.Ticket{}, errors.New("not implemented")
}

func (s fetchSource) FetchComments(context.Context, source.Ticket) ([]source.Comment, error) {
	return nil, errors.New("not implemented")
}

func (s fetchSource) RemovePlanLabel(context.Context, source.Ticket) error { return nil }

func (s fetchSource) BackToTrigger(context.Context, source.Ticket, string) error { return nil }

func (s fetchSource) MarkReady(context.Context, source.Ticket, source.ReadyInfo) error { return nil }

func (s fetchSource) Comment(context.Context, source.Ticket, string) error { return nil }
