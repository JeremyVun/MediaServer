package classify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

// gate holds chosen TVmaze requests until released, then serves them
// normally (the harness's stall answers 504, which reads as an outage).
type gate struct {
	mu      sync.Mutex
	holds   map[string]chan struct{}
	arrived chan string
}

// newGate must run before the first TVmaze request. It serves the fake
// from a new server, set up before it starts so the handler and the fake's
// image URLs are race-free, and rebuilds the service against it.
func newGate(h *harness) *gate {
	g := &gate{holds: map[string]chan struct{}{}, arrived: make(chan string, 64)}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		ch, held := g.holds[r.URL.RequestURI()]
		g.mu.Unlock()
		if held {
			select {
			case g.arrived <- r.URL.RequestURI():
			default:
			}
			<-ch
		}
		h.tv.serve(w, r)
	}))
	h.tv.srv = srv
	srv.Start()
	h.t.Cleanup(srv.Close)
	h.svc = New(Options{
		Store:  h.st,
		Bus:    h.bus,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		TVmaze: tvmaze.New(tvmaze.Options{BaseURL: srv.URL, RateLimit: 10000, RateWindow: time.Second}),
		ArtDir: h.art,
	})
	h.svc.SetScheduler(h.sched)
	return g
}

func (g *gate) hold(uri string) (release func()) {
	ch := make(chan struct{})
	g.mu.Lock()
	g.holds[uri] = ch
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

func (g *gate) wait(t *testing.T, uri string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case got := <-g.arrived:
			if got == uri {
				return
			}
		case <-timeout:
			t.Fatalf("no request for %s", uri)
		}
	}
}

func classifyAsync(h *harness, id int64) <-chan error {
	done := make(chan error, 1)
	go func() { done <- h.svc.Classify(context.Background(), id) }()
	return done
}

func await(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("classify never finished")
	}
}

// ownerAliasedShow forms an unmatched flagged show from a yearless Lioness
// file (namesakes tie) and teaches an owner alias onto it by moving an
// episode with another key there.
func ownerAliasedShow(t *testing.T, h *harness) (show store.Show, moved store.Item) {
	t.Helper()
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	show = h.show(*first.ShowID)
	if show.TVmazeID != nil || !show.NeedsCheck {
		t.Fatalf("fixture: show = %+v, want unmatched and flagged", show)
	}
	stray := h.classify(h.item("Lioness.Extras.S03E09.mkv", 3000).ID)
	moved, err := h.svc.MoveEpisode(h.ctx, stray.ID, show.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a := h.alias("lioness extras"); a.ShowID != show.ID || a.Source != store.AliasOwner {
		t.Fatalf("fixture: alias = %+v, want owner alias to show %d", a, show.ID)
	}
	return h.show(show.ID), moved
}

// probe: an automatic action never overwrites an owner alias, even when a later arrival matches the show it points at (B7.4 merge-on-match).
func TestProbe_Learning_LaterArrivalMergeKeepsOwnerAlias(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	show, moved := ownerAliasedShow(t, h)
	matched, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Special Ops: Lioness", TVmazeID: ptr(int64(50415))})
	if err != nil {
		t.Fatal(err)
	}

	h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)

	if a := h.alias("lioness extras"); a.ShowID != show.ID {
		t.Errorf("owner alias %q now points at show %d (matched show is %d), want the owner's show %d",
			a.Key, a.ShowID, matched.ID, show.ID)
	}
	if got := h.get(moved.ID); got.ShowID == nil || *got.ShowID != show.ID {
		t.Errorf("owner-moved item %d is in show %d, want the show the owner put it in (%d)", moved.ID, derefInt64(got.ShowID), show.ID)
	}
}

// probe: an automatic action never overwrites an owner alias, even when an outage-formed show is re-matched by show_refresh.
func TestProbe_Learning_OutageRematchKeepsOwnerAlias(t *testing.T) {
	h := newHarness(t)
	h.tv.down.Store(true)
	first := h.classify(h.item("Lioness.2023.S03E01.mkv", 3000).ID)
	show := h.show(*first.ShowID)
	if show.TVmazeID != nil || show.RefreshedAt != nil || !show.NeedsCheck {
		t.Fatalf("fixture: show = %+v, want an outage-formed unmatched show", show)
	}
	stray := h.classify(h.item("Lioness.Extras.S03E09.mkv", 3000).ID)
	moved, err := h.svc.MoveEpisode(h.ctx, stray.ID, show.ID)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Special Ops: Lioness", TVmazeID: ptr(int64(50415))})
	if err != nil {
		t.Fatal(err)
	}
	h.tv.down.Store(false)
	lionessShows(h)

	if err := h.svc.RefreshShow(h.ctx, show.ID); err != nil {
		t.Fatal(err)
	}
	h.invariant()

	if a := h.alias("lioness extras"); a.ShowID != show.ID {
		t.Errorf("owner alias %q now points at show %d (matched show is %d), want the owner's show %d",
			a.Key, a.ShowID, matched.ID, show.ID)
	}
	if got := h.get(moved.ID); got.ShowID == nil || *got.ShowID != show.ID {
		t.Errorf("owner-moved item %d is in show %d, want the show the owner put it in (%d)", moved.ID, derefInt64(got.ShowID), show.ID)
	}
}

// probe: "Looks right" given while a later arrival is asking TVmaze settles the show; the arrival never re-matches it (B7.4, 4c re-read).
func TestProbe_Concurrency_ConfirmDuringUpgradeLookup(t *testing.T) {
	h := newHarness(t)
	g := newGate(h)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	show := *first.ShowID
	arrival := h.item("Lioness.2023.S03E02.mkv", 3000)

	const uri = "/search/shows?q=lioness"
	release := g.hold(uri)
	done := classifyAsync(h, arrival.ID)
	g.wait(t, uri)
	if _, err := h.svc.ConfirmShow(h.ctx, show); err != nil {
		t.Fatal(err)
	}
	release()
	await(t, done)
	h.invariant()

	got := h.show(show)
	if got.TVmazeID != nil || got.NeedsCheck {
		t.Errorf("show = %+v, want it left unmatched and confirmed", got)
	}
	if item := h.get(arrival.ID); item.ShowID == nil || *item.ShowID != show {
		t.Errorf("arrival joined %v, want the confirmed show %d", item.ShowID, show)
	}
}

// probe: "Not on TVmaze" chosen while a later arrival is asking TVmaze is never undone by that arrival (B7.4).
func TestProbe_Concurrency_NotOnTVmazeDuringUpgradeLookup(t *testing.T) {
	h := newHarness(t)
	g := newGate(h)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	show := *first.ShowID
	arrival := h.item("Lioness.2023.S03E02.mkv", 3000)

	const uri = "/search/shows?q=lioness"
	release := g.hold(uri)
	done := classifyAsync(h, arrival.ID)
	g.wait(t, uri)
	if _, err := h.svc.ChangeMatch(h.ctx, show, Match{}); err != nil {
		t.Fatal(err)
	}
	release()
	await(t, done)
	h.invariant()

	if got := h.show(show); got.TVmazeID != nil || got.NeedsCheck {
		t.Errorf("show = %+v, want it to stay not on TVmaze", got)
	}
	if len(h.shows()) != 1 {
		t.Errorf("shows = %d, want 1", len(h.shows()))
	}
}

// probe: an owner move that teaches an alias while an arrival with that key is asking TVmaze wins over the arrival's lookup.
func TestProbe_Concurrency_OwnerMoveDuringLookup(t *testing.T) {
	h := newHarness(t)
	g := newGate(h)
	owner := mustShow(t, h, "Owner's Foo")
	e1 := h.item("Foo.S01E01.mkv", 1500)
	e2 := h.item("Foo.S01E02.mkv", 1500)

	const uri = "/search/shows?q=foo"
	release := g.hold(uri)
	done := classifyAsync(h, e2.ID)
	g.wait(t, uri)
	if _, err := h.svc.MoveEpisode(h.ctx, e1.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	release()
	await(t, done)
	h.invariant()

	if got := h.get(e2.ID); got.ShowID == nil || *got.ShowID != owner.ID {
		t.Errorf("arrival joined %v, want the owner's show %d", got.ShowID, owner.ID)
	}
	if n := len(h.shows()); n != 1 {
		t.Errorf("shows = %d, want only the owner's show", n)
	}
	if a := h.alias("foo"); a.ShowID != owner.ID || a.Source != store.AliasOwner {
		t.Errorf("alias = %+v, want the owner's", a)
	}
}

// probe: an owner merge while an arrival for the merged show is asking TVmaze lands the arrival in the merge target, never a revived show.
func TestProbe_Concurrency_MergeIntoMatchedDuringLookup(t *testing.T) {
	h := newHarness(t)
	g := newGate(h)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	unmatched := *first.ShowID
	target, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Lioness (ZA)", TVmazeID: ptr(int64(77))})
	if err != nil {
		t.Fatal(err)
	}
	arrival := h.item("Lioness.2023.S03E02.mkv", 3000)

	const uri = "/search/shows?q=lioness"
	release := g.hold(uri)
	done := classifyAsync(h, arrival.ID)
	g.wait(t, uri)
	if _, err := h.svc.MergeShows(h.ctx, unmatched, target.ID); err != nil {
		t.Fatal(err)
	}
	release()
	await(t, done)
	h.invariant()

	if got := h.get(arrival.ID); got.ShowID == nil || *got.ShowID != target.ID {
		t.Errorf("arrival joined %v, want the owner's merge target %d", got.ShowID, target.ID)
	}
	if n := len(h.shows()); n != 1 {
		t.Errorf("shows = %d, want 1 (the merge target)", n)
	}
	if got := h.show(target.ID); got.TVmazeID == nil || *got.TVmazeID != 77 {
		t.Errorf("merge target re-matched to %v, want its own 77", got.TVmazeID)
	}
}

// probe: art fetched for a match is never installed once the owner changed the match mid-download.
func TestProbe_Concurrency_ChangeMatchDuringArtDownload(t *testing.T) {
	h := newHarness(t)
	g := newGate(h)
	h.tv.add(andorShow(), "andor")
	h.tv.add(tvShow{id: 2, name: "Other", premiered: "2020-01-01", poster: true, backdrop: true})
	item := h.item("Andor.S01E01.mkv", 2400)

	const uri = "/img/poster-1.jpg"
	release := g.hold(uri)
	done := classifyAsync(h, item.ID)
	g.wait(t, uri)
	show := *h.get(item.ID).ShowID
	if _, err := h.svc.ChangeMatch(h.ctx, show, Match{TVmazeID: ptr(int64(2))}); err != nil {
		t.Fatal(err)
	}
	release()
	await(t, done)

	got := h.show(show)
	if got.PosterAt != nil || got.BackdropAt != nil || exists(h.svc.PosterPath(show)) || exists(h.svc.BackdropPath(show)) {
		t.Errorf("show %+v has art installed after the match changed from 1 to 2", got)
	}
}

// probe: the same item classified by several jobs at once files once, into one show (duplicate classify jobs).
func TestProbe_Invariant_ConcurrentClassifySameItem(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness)
		path  string
	}{
		{"confident", func(h *harness) { h.tv.add(andorShow(), "andor") }, "Andor.S01E02.mkv"},
		{"weak", lionessShows, "Lioness.S03E02.mkv"},
		{"no results", func(*harness) {}, "Nobody.Knows.S01E01.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			h.tv.delay = 20 * time.Millisecond
			item := h.item(tc.path, 2400)
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- h.svc.Classify(context.Background(), item.ID)
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			h.invariant()
			got := h.get(item.ID)
			if got.ClassifiedAt == nil || got.ShowID == nil {
				t.Fatalf("item = %+v, want it filed", got)
			}
			if n := len(h.shows()); n != 1 {
				t.Fatalf("shows = %d, want 1", n)
			}
		})
	}
}

// probe: a season pack of an unmatched show racing through classify forms exactly one show, with TVmaze slow enough that every lookup precedes every lock.
func TestProbe_Concurrency_SeasonPackRaceSlowTVmaze(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness)
		path  string
	}{
		{"confident", func(h *harness) { h.tv.add(andorShow(), "andor") }, "Andor.S01E%02d.mkv"},
		{"weak", lionessShows, "Lioness.S03E%02d.mkv"},
		{"no results", func(*harness) {}, "Nobody.Knows.S01E%02d.mkv"},
		{"mixed keys", func(h *harness) { lionessShows(h) }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			h.tv.delay = 30 * time.Millisecond
			var ids []int64
			for e := 1; e <= 8; e++ {
				path := fmt.Sprintf(tc.path, e)
				if tc.path == "" {
					path = fmt.Sprintf("Lioness.S03E%02d.mkv", e)
					if e%2 == 0 {
						path = fmt.Sprintf("Lioness.2023.S03E%02d.mkv", e)
					}
				}
				ids = append(ids, h.item(path, 2400).ID)
			}
			var wg sync.WaitGroup
			errs := make(chan error, len(ids))
			for _, id := range ids {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- h.svc.Classify(context.Background(), id)
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			h.invariant()
			if n := len(h.shows()); n != 1 {
				var titles []string
				for _, s := range h.shows() {
					titles = append(titles, fmt.Sprintf("%d:%s tvmaze=%v", s.ID, s.Title, s.TVmazeID))
				}
				t.Fatalf("shows = %v, want exactly one", titles)
			}
			for _, id := range ids {
				if got := h.get(id); got.ShowID == nil {
					t.Fatalf("item %d not filed", id)
				}
			}
		})
	}
}

// probe: a weak later arrival never merges a flagged unmatched show into the local show holding its weak candidate (M3/M11 on the B7.4 path).
func TestProbe_Weak_LaterArrivalWeakNeverMerges(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	matched, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Special Ops: Lioness", TVmazeID: ptr(int64(50415))})
	if err != nil {
		t.Fatal(err)
	}
	second := h.classify(h.item("Lioness.S03E03.mkv", 3000).ID)
	if *second.ShowID != *first.ShowID || *second.ShowID == matched.ID {
		t.Errorf("weak arrival joined %d, want the unmatched show %d", *second.ShowID, *first.ShowID)
	}
	if got := h.show(*first.ShowID); got.TVmazeID != nil || !got.NeedsCheck {
		t.Errorf("unmatched show = %+v, want it still unmatched and flagged", got)
	}
}

// probe: a weak result never attaches through show_refresh's outage re-match either.
func TestProbe_Weak_OutageRematchNeverAttachesWeak(t *testing.T) {
	h := newHarness(t)
	h.tv.down.Store(true)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	show := *first.ShowID
	if _, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Special Ops: Lioness", TVmazeID: ptr(int64(50415))}); err != nil {
		t.Fatal(err)
	}
	h.tv.down.Store(false)
	lionessShows(h)
	if err := h.svc.RefreshShow(h.ctx, show); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetShow(h.ctx, show)
	if errors.Is(err, store.ErrNotFound) {
		t.Fatalf("outage show %d merged away on a weak re-match", show)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got.TVmazeID != nil || !got.NeedsCheck {
		t.Errorf("show = %+v, want it unmatched and flagged after a weak re-match", got)
	}
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// probe: later arrivals that can match a flagged unmatched show, racing each other and an arrival under another key for the same TVmaze show, leave exactly one show holding the match (B7.4 vs concurrent arrivals).
func TestProbe_Concurrency_UpgradeRacesOtherKeys(t *testing.T) {
	for run := 0; run < 5; run++ {
		h := newHarness(t)
		lionessShows(h)
		h.tv.delay = 15 * time.Millisecond
		first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
		var ids []int64
		for _, p := range []string{"Lioness.2023.S03E02.mkv", "Lioness.2023.S03E03.mkv", "Lioness.S03E04.mkv", "Lioness.US.S03E05.mkv", "Lioness.US.S03E06.mkv"} {
			ids = append(ids, h.item(p, 3000).ID)
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(ids))
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- h.svc.Classify(context.Background(), id)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		h.invariant()
		shows := h.shows()
		if len(shows) != 1 || shows[0].TVmazeID == nil || *shows[0].TVmazeID != 50415 {
			var got []string
			for _, s := range shows {
				got = append(got, fmt.Sprintf("%d:%s tvmaze=%d", s.ID, s.Title, derefInt64(s.TVmazeID)))
			}
			t.Fatalf("run %d: shows = %v, want one show matched to 50415", run, got)
		}
		for _, id := range append(ids, first.ID) {
			if got := h.get(id); got.ShowID == nil || *got.ShowID != shows[0].ID {
				t.Fatalf("run %d: item %d in show %d, want %d", run, id, derefInt64(got.ShowID), shows[0].ID)
			}
		}
	}
}
