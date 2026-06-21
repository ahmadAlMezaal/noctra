package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDrainAndStop_CancelsBeforeWait guards the dispatch-cap deadlock fix: a
// self-shutdown that calls wg.Wait() without first cancelling the context-bound
// child goroutines blocks forever. If stop() is dropped or reordered after the
// wait, the child below never returns and this test times out.
func TestDrainAndStop_CancelsBeforeWait(t *testing.T) {
	loopCtx, stop := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-loopCtx.Done()
	}()

	done := make(chan struct{})
	go func() {
		drainAndStop(stop, &wg)
		close(done)
	}()

	select {
	case <-done:
		// drainAndStop returned — stop() was called before wg.Wait() drained.
	case <-time.After(2 * time.Second):
		t.Fatal("drainAndStop deadlocked: stop() must be called before wg.Wait() " +
			"so context-bound child goroutines can leave the WaitGroup")
	}
}
