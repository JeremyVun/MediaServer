package classify

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

// waitStalled blocks until the fake holds a request for uri.
func waitStalled(t *testing.T, h *harness, uri string) {
	t.Helper()
	select {
	case got := <-h.tv.stalled:
		if got != uri {
			t.Fatalf("stalled %s, want %s", got, uri)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no request for %s reached TVmaze", uri)
	}
}

// TestStalledTVmazeNeverHoldsTheLock reproduces the dry run's timeouts:
// TVmaze's origin left one request unanswered while it answered others. The
// stalled job must not keep other arrivals, refreshes or owner edits
// waiting on the classifier lock.
func TestStalledTVmazeNeverHoldsTheLock(t *testing.T) {
	quick := func(t *testing.T, h *harness, what string, fn func(context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := fn(ctx); err != nil {
			t.Fatalf("%s while TVmaze stalls another request: %v", what, err)
		}
	}
	lucky := tvShow{id: 81228, name: "Lucky", premiered: "2026-07-15", episodes: seasons(7), poster: true}

	cases := []struct {
		name  string
		stall string
		setup func(h *harness) func(context.Context) error
	}{
		{"search", "/search/shows?q=silo", func(h *harness) func(context.Context) error {
			item := h.item("Silo.S02E07.mkv", 3000)
			return func(ctx context.Context) error { return h.svc.Classify(ctx, item.ID) }
		}},
		{"namesake episode probe", "/shows/81119/episodes?specials=1", func(h *harness) func(context.Context) error {
			h.tv.add(tvShow{id: 38052, name: "Silo", premiered: "2023-05-04", episodes: seasons(10, 10)}, "silo")
			h.tv.add(tvShow{id: 81119, name: "Silo", premiered: "2017-01-01", country: "SG", episodes: seasons(6)}, "silo")
			item := h.item("Silo.S02E07.mkv", 3000)
			return func(ctx context.Context) error { return h.svc.Classify(ctx, item.ID) }
		}},
		{"show refresh", "/shows/38052", func(h *harness) func(context.Context) error {
			h.tv.add(tvShow{id: 38052, name: "Silo", premiered: "2023-05-04", episodes: seasons(10, 10)}, "silo")
			show := h.show(*h.classify(h.item("Silo.S02E07.mkv", 3000).ID).ShowID)
			return func(ctx context.Context) error { return h.svc.RefreshShow(ctx, show.ID) }
		}},
		{"poster download", "/img/poster-38052.jpg", func(h *harness) func(context.Context) error {
			h.tv.add(tvShow{id: 38052, name: "Silo", premiered: "2023-05-04", episodes: seasons(10, 10), poster: true}, "silo")
			item := h.item("Silo.S02E07.mkv", 3000)
			return func(ctx context.Context) error { return h.svc.Classify(ctx, item.ID) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.tv.add(lucky, "lucky")
			stalledJob := tc.setup(h)
			edited := h.classify(h.item("Clip.mkv", 60).ID)
			release := h.tv.stall(tc.stall)
			defer release()

			done := make(chan error, 1)
			go func() { done <- stalledJob(context.Background()) }()
			waitStalled(t, h, tc.stall)

			lucky := h.item("Lucky.S01E02.mkv", 2400)
			quick(t, h, "classify", func(ctx context.Context) error { return h.svc.Classify(ctx, lucky.ID) })
			if got := h.get(lucky.ID); got.ShowID == nil || h.show(*got.ShowID).TVmazeID == nil || got.Title != "S1E2" {
				t.Fatalf("Lucky = %+v, want it filed under its TVmaze match", got)
			}
			quick(t, h, "owner edit", func(ctx context.Context) error {
				_, err := h.svc.EditItem(ctx, edited.ID, ItemEdit{Title: ptr("Renamed")})
				return err
			})
			select {
			case err := <-done:
				t.Fatalf("stalled job finished early: %v", err)
			default:
			}

			release()
			if err := <-done; err != nil && !errors.Is(err, ErrUnreachable) {
				t.Fatalf("stalled job: %v", err)
			}
			h.invariant()
		})
	}
}

func TestRepeatArrivalsAskTVmazeOnce(t *testing.T) {
	t.Run("weak by year", func(t *testing.T) {
		h := newHarness(t)
		h.tv.maxAge = 3600
		h.tv.add(tvShow{id: 8244, name: "Monster", premiered: "2004-04-07", episodes: seasons(74)}, "monster")
		var show int64
		for e := 1; e <= 9; e++ {
			item := h.classify(h.item(fmt.Sprintf("Monster.2022.S02E%02d.mkv", e), 3000).ID)
			show = *item.ShowID
		}
		if got := h.show(show); got.TVmazeID != nil || !got.NeedsCheck || len(h.shows()) != 1 {
			t.Fatalf("show = %+v, want one unmatched flagged Monster", got)
		}
		if n := h.tv.requests("/"); n != 1 {
			t.Fatalf("nine arrivals made %d TVmaze requests, want the one search", n)
		}
	})

	t.Run("namesakes tied by episode", func(t *testing.T) {
		h := newHarness(t)
		h.tv.maxAge = 3600
		for i, year := range []string{"2026", "1981", "1955"} {
			h.tv.add(tvShow{id: 100 + i, name: "East of Eden", premiered: year + "-01-01", episodes: seasons(8)}, "east of eden")
		}
		for e := 1; e <= 7; e++ {
			h.classify(h.item(fmt.Sprintf("East of Eden - S01E%02d.mkv", e), 3000).ID)
		}
		if n, eps := h.tv.requests("/search/"), h.tv.requests("/shows/"); n != 1 || eps != 3 {
			t.Fatalf("seven arrivals made %d searches and %d episode lists, want 1 and 3", n, eps)
		}
		if len(h.shows()) != 1 || h.shows()[0].TVmazeID != nil {
			t.Fatalf("shows = %+v, want one unmatched East of Eden", h.shows())
		}
	})

	t.Run("a year is new information", func(t *testing.T) {
		h := newHarness(t)
		h.tv.maxAge = 3600
		lionessShows(h)
		first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
		if show := h.show(*first.ShowID); show.TVmazeID != nil {
			t.Fatalf("yearless first file matched %v, want unmatched", *show.TVmazeID)
		}
		h.classify(h.item("Lioness.S03E04.mkv", 3000).ID)
		second := h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)
		show := h.show(*second.ShowID)
		if show.ID != *first.ShowID || show.TVmazeID == nil || *show.TVmazeID != 50415 || show.NeedsCheck {
			t.Fatalf("show = %+v, want the 2023 file to match the same show to Lioness 2023 (B7 item 4)", show)
		}
		if n := h.tv.requests("/search/"); n != 1 {
			t.Fatalf("%d searches, want one: TVmaze search ignores the year", n)
		}
		if n := h.tv.requests("/shows/50415/episodes"); n != 1 {
			t.Fatalf("Lioness's episode list fetched %d times, want once for the tie-break and the match", n)
		}
	})

	t.Run("a country is a new query", func(t *testing.T) {
		h := newHarness(t)
		h.tv.maxAge = 3600
		h.tv.add(tvShow{id: 28826, name: "Euphoria", premiered: "2019-06-16", country: "US", episodes: seasons(8, 8)}, "euphoria")
		h.tv.add(tvShow{id: 4011, name: "Euphoria", premiered: "2012-11-11", country: "IL", episodes: seasons(10)}, "euphoria")
		h.classify(h.item("Euphoria (2019) - S01E01.mkv", 3300).ID)
		h.classify(h.item("Euphoria (2019) - S01E02.mkv", 3300).ID)
		us := h.classify(h.item("Euphoria.US.S02E03.mkv", 3300).ID)
		if got := h.tv.searched(); fmt.Sprint(got) != "[euphoria euphoria us]" {
			t.Fatalf("searches = %v, want euphoria once, then euphoria us", got)
		}
		if show := h.show(*us.ShowID); show.TVmazeID == nil || *show.TVmazeID != 28826 || len(h.shows()) != 1 {
			t.Fatalf("Euphoria US joined %+v, want the one Euphoria", show)
		}
		if a := h.alias("euphoria us"); a.Source != store.AliasAuto {
			t.Fatalf("alias = %+v", a)
		}
	})
}

// TestCatalogChangeWhileAskingTVmaze: the lookup found the match already
// local, so it fetched no metadata; by the time the lock was free that show
// was gone. The decision must ask again rather than form a bare show.
func TestCatalogChangeWhileAskingTVmaze(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	local, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Andor", TVmazeID: ptr(int64(1))})
	if err != nil {
		t.Fatal(err)
	}
	item := h.item("Andor.S01E03.mkv", 2400)

	if err := h.svc.lock(h.ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.svc.Classify(h.ctx, item.ID) }()
	for deadline := time.Now().Add(5 * time.Second); h.tv.requests("/search/") == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("classify never searched")
		}
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := h.st.DeleteShow(h.ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	h.svc.unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	got := h.get(item.ID)
	show := h.show(*got.ShowID)
	if show.TVmazeID == nil || *show.TVmazeID != 1 || show.RefreshedAt == nil || got.Title != "Chapter 3" {
		t.Fatalf("show = %+v item = %+v, want a new matched show with TVmaze's episodes", show, got)
	}
	if n := h.tv.requests("/search/"); n != 2 {
		t.Fatalf("%d searches, want the stale decision to ask again", n)
	}
}
