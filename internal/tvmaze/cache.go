package tvmaze

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxCacheAge        = time.Hour
	maxCachedResponses = 2048
)

// responses keeps API answers for as long as TVmaze marks them fresh
// (Cache-Control max-age, an hour), so a burst of arrivals for one show asks
// once, and lets concurrent callers of one URL share a single request.
type responses struct {
	mu      sync.Mutex
	fresh   map[string]response
	pending map[string]*call
	now     func() time.Time
}

type response struct {
	body    []byte
	expires time.Time
}

type call struct {
	done chan struct{}
	body []byte
	err  error
	// The caller that made the request gave up, so its error says nothing
	// about TVmaze; waiters ask again themselves.
	abandoned bool
}

func newResponses() *responses {
	return &responses{fresh: map[string]response{}, pending: map[string]*call{}, now: time.Now}
}

// get returns url's body from the cache, else from the request in flight,
// else from fetch, which also says how long the body stays fresh.
func (r *responses) get(ctx context.Context, url string, fetch func(context.Context) ([]byte, time.Duration, error)) ([]byte, error) {
	for {
		r.mu.Lock()
		if res, ok := r.fresh[url]; ok && r.now().Before(res.expires) {
			r.mu.Unlock()
			return res.body, nil
		}
		if c, ok := r.pending[url]; ok {
			r.mu.Unlock()
			select {
			case <-c.done:
			case <-ctx.Done():
				return nil, fmt.Errorf("tvmaze: GET %s: %w", url, ctx.Err())
			}
			if c.abandoned {
				continue
			}
			return c.body, c.err
		}
		c := &call{done: make(chan struct{})}
		r.pending[url] = c
		r.mu.Unlock()

		body, ttl, err := fetch(ctx)
		r.mu.Lock()
		delete(r.pending, url)
		if err == nil && ttl > 0 {
			r.store(url, body, ttl)
		}
		r.mu.Unlock()
		c.body, c.err, c.abandoned = body, err, ctx.Err() != nil
		close(c.done)
		return body, err
	}
}

func (r *responses) store(url string, body []byte, ttl time.Duration) {
	now := r.now()
	for u, res := range r.fresh {
		if !now.Before(res.expires) {
			delete(r.fresh, u)
		}
	}
	if len(r.fresh) < maxCachedResponses {
		r.fresh[url] = response{body: body, expires: now.Add(ttl)}
	}
}

// freshFor reads Cache-Control: max-age, capped at maxCacheAge; 0 means do
// not keep the response.
func freshFor(h http.Header) time.Duration {
	var age time.Duration
	for _, directive := range strings.Split(h.Get("Cache-Control"), ",") {
		directive = strings.ToLower(strings.TrimSpace(directive))
		switch {
		case directive == "no-store" || directive == "no-cache":
			return 0
		case strings.HasPrefix(directive, "max-age="):
			if secs, err := strconv.Atoi(strings.TrimPrefix(directive, "max-age=")); err == nil && secs > 0 {
				age = time.Duration(secs) * time.Second
			}
		}
	}
	return min(age, maxCacheAge)
}
