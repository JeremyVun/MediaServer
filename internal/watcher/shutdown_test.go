package watcher

import (
	"context"
	"testing"
	"time"
)

// TestManagerWaitReturnsAfterCancel verifies the mount/volumes loops and any
// per-root watchers exit and are joined once the Start context is cancelled,
// so main can wait for them before closing the DB.
func TestManagerWaitReturnsAfterCancel(t *testing.T) {
	st, closeStore := newWatcherStore(t)
	defer closeStore()

	m := NewManager(Options{
		Store:         st,
		Jobs:          newFakeJobs(),
		StreamFactory: newFakeStreams().factory,
	})

	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
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
