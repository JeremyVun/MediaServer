package tvmaze

import (
	"context"
	"sync"
	"time"
)

// limiter allows at most limit requests in any rolling window, shared by
// every goroutine using the client.
type limiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	sent        []time.Time
	pausedUntil time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, sent: make([]time.Time, 0, limit)}
}

func (l *limiter) wait(ctx context.Context) error {
	for {
		delay := l.reserve(time.Now())
		if delay == 0 {
			return nil
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// reserve records a request at now and returns 0, or returns how long to
// wait before trying again.
func (l *limiter) reserve(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.pausedUntil) {
		return l.pausedUntil.Sub(now)
	}
	if len(l.sent) == l.limit {
		if free := l.sent[0].Add(l.window); now.Before(free) {
			return free.Sub(now)
		}
		l.sent = append(l.sent[:0], l.sent[1:]...)
	}
	l.sent = append(l.sent, now)
	return 0
}

// pause holds every caller back, as a 429 applies to the whole client.
func (l *limiter) pause(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := time.Now().Add(d); until.After(l.pausedUntil) {
		l.pausedUntil = until
	}
}
