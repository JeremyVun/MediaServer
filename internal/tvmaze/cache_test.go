package tvmaze

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFreshResponsesAreReused(t *testing.T) {
	var calls atomic.Int32
	cacheControl, status := "public, max-age=60", http.StatusOK
	body := `[]`
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", cacheControl)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	now := time.Now()
	c.responses.now = func() time.Time { return now }
	search := func(q string) error {
		_, err := c.Search(context.Background(), q)
		return err
	}
	expect := func(what string, want int32) {
		t.Helper()
		if got := calls.Swap(0); got != want {
			t.Errorf("%s: %d requests, want %d", what, got, want)
		}
	}

	search("a")
	search("a")
	expect("fresh repeat", 1)
	search("b")
	expect("another URL", 1)
	now = now.Add(61 * time.Second)
	search("a")
	expect("after max-age", 1)

	cacheControl = "public, max-age=86400"
	search("long")
	now = now.Add(maxCacheAge - time.Second)
	search("long")
	expect("within the cap", 1)
	now = now.Add(2 * time.Second)
	search("long")
	expect("max-age is capped at an hour", 1)

	for _, cc := range []string{"", "no-cache", "max-age=0", "no-store, max-age=60"} {
		cacheControl = cc
		search("x " + cc)
		search("x " + cc)
		expect("Cache-Control "+cc, 2)
	}

	cacheControl, status = "public, max-age=60", http.StatusBadGateway
	if err := search("down"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
	status = http.StatusOK
	search("down")
	expect("an error is not kept", 2)

	body = `[{"score":`
	search("torn")
	search("torn")
	expect("a malformed body is not kept", 2)
}

func TestConcurrentCallsShareOneRequest(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		w.Write(fixture(t, "search_andor.json"))
	}))
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if got, err := c.Search(context.Background(), "andor"); err != nil || len(got) != 3 {
				t.Errorf("Search = %d results, %v", len(got), err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("five concurrent searches made %d requests, want 1", calls.Load())
	}
}

// TestStalledRequestTimesOutAlone is the dry run's failure: TVmaze's origin
// held one request without answering while it answered others on the same
// connection.
func TestStalledRequestTimesOutAlone(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/shows/81119/episodes" {
			<-r.Context().Done()
			return
		}
		w.Write([]byte(`[]`))
	}))
	c = New(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Timeout: 300 * time.Millisecond})

	stalled := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := c.Episodes(context.Background(), 81119)
		stalled <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := c.Search(context.Background(), "silo"); err != nil {
		t.Fatalf("search beside a stalled request: %v", err)
	}
	select {
	case err := <-stalled:
		t.Fatalf("stalled request returned before its timeout: %v", err)
	default:
	}

	waiter, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() {
		_, err := c.Episodes(waiter, 81119)
		waited <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-waited; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter = %v, want context.Canceled", err)
	}

	err := <-stalled
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("stalled request = %v, want ErrUnreachable", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("stalled request gave up after %s, want the 300ms timeout", elapsed)
	}
}

func TestAbandonedRequestIsAskedAgain(t *testing.T) {
	var calls atomic.Int32
	first := make(chan struct{})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(first)
			<-r.Context().Done()
			return
		}
		w.Write([]byte(`[]`))
	}))
	leader, cancel := context.WithCancel(context.Background())
	led := make(chan error, 1)
	go func() {
		_, err := c.Search(leader, "x")
		led <- err
	}()
	<-first
	followed := make(chan error, 1)
	go func() {
		_, err := c.Search(context.Background(), "x")
		followed <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-led; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader = %v, want context.Canceled", err)
	}
	if err := <-followed; err != nil {
		t.Fatalf("waiter inherited the leader's cancellation: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d requests, want the waiter to ask again", calls.Load())
	}
}
