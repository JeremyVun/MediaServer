package jobs

import (
	"context"
	"testing"
	"time"
)

// TestManagerWaitReturnsAfterCancel verifies the graceful-shutdown contract:
// once the Start context is cancelled, Wait returns promptly so main can join
// the workers before closing the DB.
func TestManagerWaitReturnsAfterCancel(t *testing.T) {
	st := newTestStore(t)
	m := NewManager(Options{Store: st, Workers: 3, CleanupInterval: 0})

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	cancel()

	done := make(chan struct{})
	go func() {
		m.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return within 3s after cancel")
	}
}
