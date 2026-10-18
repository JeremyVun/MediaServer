package classify

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tmdb"
)

const dummyToken = "test-read-access-token-91c2"

type tmdbMovie struct {
	id                 int
	title, original    string
	released, overview string
	backdrop, poster   string
	runtime            int // minutes; only details carry it, as on TMDB
}

// fakeTMDB serves the slice of the TMDB API and image CDN classify uses.
type fakeTMDB struct {
	srv    *httptest.Server
	mu     sync.Mutex
	movies map[int]tmdbMovie
	search map[string][]int // "query|year" → ids in TMDB's order
	uris   []string
	auths  map[string]string // uri → Authorization header
	down   atomic.Bool
	hits   atomic.Int64
}

func newFakeTMDB(t *testing.T) *fakeTMDB {
	f := &fakeTMDB{movies: map[int]tmdbMovie{}, search: map[string][]int{}, auths: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTMDB) add(m tmdbMovie, searches ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.movies[m.id] = m
	for _, q := range searches {
		f.search[q] = append(f.search[q], m.id)
	}
}

func (f *fakeTMDB) requests(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, u := range f.uris {
		if strings.HasPrefix(u, prefix) {
			out = append(out, u)
		}
	}
	return out
}

func (f *fakeTMDB) serve(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	f.mu.Lock()
	f.uris = append(f.uris, r.URL.RequestURI())
	f.auths[r.URL.RequestURI()] = r.Header.Get("Authorization")
	f.mu.Unlock()
	if f.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	isAPI := strings.HasPrefix(r.URL.Path, "/3/")
	if isAPI && r.Header.Get("Authorization") != "Bearer "+dummyToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/3/search/movie":
		key := r.URL.Query().Get("query") + "|" + r.URL.Query().Get("year")
		results := []any{}
		for _, id := range f.search[key] {
			results = append(results, f.movieJSON(f.movies[id]))
		}
		writeJSON(w, map[string]any{"page": 1, "results": results})
	case strings.HasPrefix(r.URL.Path, "/3/movie/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/3/movie/"))
		m, ok := f.movies[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		details := f.movieJSON(m)
		details["runtime"] = m.runtime
		writeJSON(w, details)
	case strings.HasPrefix(r.URL.Path, "/img/w1280/"):
		w.Write([]byte("backdrop " + r.URL.Path))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTMDB) movieJSON(m tmdbMovie) map[string]any {
	out := map[string]any{"id": m.id, "title": m.title, "original_title": m.original, "release_date": m.released,
		"overview": m.overview, "backdrop_path": nil, "poster_path": nil}
	if m.backdrop != "" {
		out["backdrop_path"] = m.backdrop
	}
	if m.poster != "" {
		out["poster_path"] = m.poster
	}
	if m.original == "" {
		out["original_title"] = m.title
	}
	return out
}

type movieHarness struct {
	*harness
	tm   *fakeTMDB
	logs *syncBuffer
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newMovieHarness(t *testing.T, token string, opts ...harnessOpt) *movieHarness {
	t.Helper()
	tm := newFakeTMDB(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := tmdb.New(tmdb.Options{BaseURL: tm.srv.URL, ImageBaseURL: tm.srv.URL + "/img/", Token: token,
		RateLimit: 10000, Log: log})
	opts = append([]harnessOpt{func(o *Options) { o.TMDB, o.Log = client, log }}, opts...)
	return &movieHarness{harness: newHarness(t, opts...), tm: tm, logs: logs}
}

func (h *movieHarness) assertTokenHidden() {
	h.t.Helper()
	if strings.Contains(h.logs.String(), dummyToken) {
		h.t.Error("token in log output")
	}
	for _, u := range h.tm.requests("") {
		if strings.Contains(u, dummyToken) {
			h.t.Errorf("token in request URL %q", u)
		}
	}
}

var dune = tmdbMovie{id: 438631, title: "Dune", released: "2021-09-15", overview: "Paul Atreides, a brilliant young man…",
	backdrop: "/dune-back.jpg", poster: "/dune-poster.jpg"}

const movieLength = 2 * 3600

func TestMovieConfidentMatch(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(dune, "Dune|2021")
	h.tm.add(tmdbMovie{id: 841, title: "Dune", released: "1984-12-14"}, "Dune|2021")
	item := h.item("movies/Dune.2021.1080p.BluRay.x264.mkv", movieLength)
	got := h.classify(item.ID)

	if got.Type != "movie" || got.TMDBID == nil || *got.TMDBID != 438631 {
		t.Fatalf("item = %+v, want movie matched to 438631", got)
	}
	if got.Summary == nil || *got.Summary != dune.overview {
		t.Errorf("summary = %v", got.Summary)
	}
	if got.ArtAt == nil || got.TMDBCheckedAt == nil {
		t.Errorf("art_at = %v, tmdb_checked_at = %v", got.ArtAt, got.TMDBCheckedAt)
	}
	if b, err := os.ReadFile(h.svc.ArtPath(item.ID)); err != nil || string(b) != "backdrop /img/w1280/dune-back.jpg" {
		t.Errorf("art = %q, %v", b, err)
	}
	if got.NeedsCheck {
		t.Error("a confident match flagged the movie")
	}
	if n := len(h.tm.requests("/3/movie/")); n != 0 {
		t.Errorf("%d detail requests; the search result carries everything", n)
	}
	h.tm.mu.Lock()
	for uri, auth := range h.tm.auths {
		if strings.HasPrefix(uri, "/img/") && auth != "" {
			t.Errorf("image request %s carried Authorization", uri)
		}
	}
	h.tm.mu.Unlock()
	if !itemEvent(h.drain(), item.ID) {
		t.Error("no item.updated")
	}
	h.assertTokenHidden()
	if !strings.Contains(h.logs.String(), "tmdb request") {
		t.Error("no tmdb request debug line")
	}
}

func TestMovieWithoutYearIsNeverMatched(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(tmdbMovie{id: 149, title: "Akira", released: "1988-07-16"}, "Akira|")
	item := h.item("movies/Akira.mkv", movieLength)
	got := h.classify(item.ID)
	if got.TMDBID != nil || !got.NeedsCheck || got.TMDBCheckedAt == nil {
		t.Errorf("item = %+v, want unmatched, flagged, checked", got)
	}
	if n := h.tm.hits.Load(); n != 0 {
		t.Errorf("%d TMDB requests for a movie that can never be confident", n)
	}
}

func TestMovieWeakCandidateNotAttached(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(tmdbMovie{id: 1, title: "Forbidden Fruit", released: "2024-01-01", backdrop: "/x.jpg"}, "Forbidden Fruits|2024")
	h.tm.add(tmdbMovie{id: 2, title: "Forbidden Fruits", released: "2022-05-01", backdrop: "/y.jpg"}, "Forbidden Fruits|2024")
	item := h.item("movies/Forbidden.Fruits.2024.mkv", movieLength)
	got := h.classify(item.ID)
	if got.TMDBID != nil || got.ArtAt != nil || got.Summary != nil {
		t.Errorf("weak candidate attached: %+v", got)
	}
	if !got.NeedsCheck || got.TMDBCheckedAt == nil {
		t.Errorf("needs_check = %v, tmdb_checked_at = %v", got.NeedsCheck, got.TMDBCheckedAt)
	}
	if exists(h.svc.ArtPath(item.ID)) || len(h.tm.requests("/img/")) != 0 {
		t.Error("art fetched for a weak candidate")
	}
}

func TestMovieMatchesOriginalTitle(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(tmdbMovie{id: 7, title: "Spirited Away", original: "千と千尋の神隠し", released: "2001-07-20"}, "千と千尋の神隠し|2001")
	item := h.item("movies/千と千尋の神隠し (2001).mkv", movieLength)
	if got := h.classify(item.ID); got.TMDBID == nil || *got.TMDBID != 7 {
		t.Errorf("tmdb_id = %v, want 7", got.TMDBID)
	}
}

func TestTokenAbsentMakesNoRequest(t *testing.T) {
	for name, token := range map[string]string{"empty": "", "blank": "  "} {
		t.Run(name, func(t *testing.T) {
			h := newMovieHarness(t, token)
			h.tm.add(dune, "Dune|2021")
			item := h.item("movies/Dune.2021.mkv", movieLength)
			got := h.classify(item.ID)
			if got.TMDBID != nil || got.TMDBCheckedAt != nil || got.NeedsCheck {
				t.Errorf("item = %+v, want untouched by TMDB", got)
			}
			if err := h.svc.RefreshMovie(h.ctx, item.ID); err != nil {
				t.Fatal(err)
			}
			if n := h.tm.hits.Load(); n != 0 {
				t.Errorf("%d TMDB requests without a token", n)
			}
			if len(h.sched.movieRuns()) != 0 {
				t.Error("movie refresh scheduled without a token")
			}
		})
	}
	h := newHarness(t)
	item := h.item("movies/Dune.2021.mkv", movieLength)
	if got := h.classify(item.ID); got.TMDBCheckedAt != nil {
		t.Errorf("nil client touched the item: %+v", got)
	}
}

func TestTMDBUnreachableRetriesLater(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(dune, "Dune|2021")
	h.tm.down.Store(true)
	item := h.item("movies/Dune.2021.mkv", movieLength)
	got := h.classify(item.ID)
	if got.ClassifiedAt == nil || got.Type != "movie" {
		t.Fatalf("classify did not finish: %+v", got)
	}
	if got.TMDBID != nil || got.TMDBCheckedAt != nil {
		t.Errorf("outage settled matching: %+v", got)
	}
	runs := h.sched.movieRuns()
	if len(runs) != 1 || runs[0].showID != item.ID || time.Until(runs[0].at) < 50*time.Minute {
		t.Fatalf("movie refresh runs = %+v, want one an hour out", runs)
	}
	if err := h.svc.RefreshMovie(h.ctx, item.ID); !errors.Is(err, ErrTMDBUnreachable) {
		t.Errorf("refresh during outage: %v", err)
	}

	h.tm.down.Store(false)
	if err := h.svc.RefreshMovie(h.ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	got = h.get(item.ID)
	if got.TMDBID == nil || *got.TMDBID != int64(dune.id) || got.ArtAt == nil || got.TMDBCheckedAt == nil {
		t.Errorf("after recovery: %+v", got)
	}
	h.assertTokenHidden()
}

func TestLockedMovieKeepsItsSummary(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(dune, "Dune|2021")
	item := h.item("movies/Dune.2021.mkv", movieLength)
	locked := true
	if _, err := h.st.UpdateItem(h.ctx, item.ID, store.UpdateItemParams{Locked: &locked}); err != nil {
		t.Fatal(err)
	}
	got := h.classify(item.ID)
	if got.TMDBID == nil || got.ArtAt == nil {
		t.Errorf("locked movie not matched: %+v", got)
	}
	if got.Summary != nil {
		t.Errorf("locked movie's summary filled: %q", *got.Summary)
	}

	owned := h.item("movies/Dune (2021).mp4", movieLength)
	if _, err := h.svc.EditItem(h.ctx, owned.ID, ItemEdit{Summary: ptr("Mine.")}); err != nil {
		t.Fatal(err)
	}
	got = h.classify(owned.ID)
	if got.TMDBID == nil || got.Summary == nil || *got.Summary != "Mine." || !got.SummaryLocked {
		t.Errorf("owner summary replaced: %+v", got)
	}
}

func TestNonMoviesNeverAskTMDB(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(dune, "Dune|2021")
	h.classify(h.item("clips/Dune.2021.mkv", 600).ID)
	h.classify(h.item("Andor/Andor.S01E01.mkv", 2400).ID)
	if n := h.tm.hits.Load(); n != 0 {
		t.Errorf("%d TMDB requests for a video and an episode", n)
	}
}

func TestChooseMovieMatch(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(tmdbMovie{id: 55, title: "Forbidden Fruits", released: "2023-05-01", overview: "A cult of girls.", backdrop: "/ff.jpg"})
	item := h.item("movies/Forbidden.Fruits.2024.mkv", movieLength)
	if got := h.classify(item.ID); got.TMDBID != nil || !got.NeedsCheck {
		t.Fatalf("precondition: %+v", got)
	}

	got, err := h.svc.ChooseMovieMatch(h.ctx, item.ID, ptr[int64](55))
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID == nil || *got.TMDBID != 55 || got.NeedsCheck {
		t.Errorf("after choosing: %+v", got)
	}
	runs := h.sched.movieRuns()
	if len(runs) != 1 || runs[0].showID != item.ID || time.Until(runs[0].at) > time.Minute {
		t.Fatalf("refresh runs = %+v, want one now", runs)
	}
	if err := h.svc.RefreshMovie(h.ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	got = h.get(item.ID)
	if got.Summary == nil || *got.Summary != "A cult of girls." || got.ArtAt == nil || !exists(h.svc.ArtPath(item.ID)) {
		t.Errorf("refresh after choosing: %+v", got)
	}

	got, err = h.svc.ChooseMovieMatch(h.ctx, item.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID != nil || got.ArtAt != nil || got.Summary != nil || got.NeedsCheck || got.TMDBCheckedAt == nil {
		t.Errorf("not on TMDB: %+v", got)
	}
	if exists(h.svc.ArtPath(item.ID)) {
		t.Error("art left behind")
	}
	if len(h.sched.movieRuns()) != 1 {
		t.Error("not on TMDB queued a refresh")
	}

	video := h.item("clips/clip.mkv", 60)
	h.classify(video.ID)
	if _, err := h.svc.ChooseMovieMatch(h.ctx, video.ID, ptr[int64](55)); !errors.Is(err, store.ErrInvalidInput) {
		t.Errorf("matching a video: %v", err)
	}
	if _, err := h.svc.ChooseMovieMatch(h.ctx, 9999, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown item: %v", err)
	}
	h.assertTokenHidden()
}

func TestChooseMovieMatchNeedsToken(t *testing.T) {
	h := newMovieHarness(t, "")
	item := h.item("movies/Dune.2021.mkv", movieLength)
	h.classify(item.ID)
	if _, err := h.svc.ChooseMovieMatch(h.ctx, item.ID, ptr[int64](1)); !errors.Is(err, ErrTMDBNotConfigured) {
		t.Errorf("err = %v", err)
	}
	if got, err := h.svc.ChooseMovieMatch(h.ctx, item.ID, nil); err != nil || got.TMDBCheckedAt == nil {
		t.Errorf("not on TMDB without a token: %+v, %v", got, err)
	}
}

func TestLeavingMovieDropsTMDBData(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(dune, "Dune|2021")
	item := h.item("movies/Dune.2021.mkv", movieLength)
	h.classify(item.ID)
	got, err := h.svc.EditItem(h.ctx, item.ID, ItemEdit{Type: ptr("video")})
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID != nil || got.ArtAt != nil || got.TMDBCheckedAt != nil || got.Summary != nil {
		t.Errorf("video kept TMDB data: %+v", got)
	}
	if exists(h.svc.ArtPath(item.ID)) {
		t.Error("art left behind")
	}
	if _, err := h.svc.EditItem(h.ctx, item.ID, ItemEdit{Type: ptr("movie")}); err != nil {
		t.Fatal(err)
	}
	if runs := h.sched.movieRuns(); len(runs) != 1 || runs[0].showID != item.ID {
		t.Errorf("becoming a movie queued %+v", runs)
	}
}

func TestHardDeleteRemovesItemArt(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(dune, "Dune|2021")
	item := h.item("movies/Dune.2021.mkv", movieLength)
	h.classify(item.ID)
	if !exists(h.svc.ArtPath(item.ID)) {
		t.Fatal("precondition: no art")
	}
	if err := h.svc.HardDeleteItem(h.ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if exists(h.svc.ArtPath(item.ID)) {
		t.Error("art survived the purge")
	}
}

func TestReclassifyClearsStaleYear(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(tmdbMovie{id: 335984, title: "Blade Runner 2049", released: "2017-10-04"}, "Blade Runner 2049|2049", "Blade Runner 2049|")
	stale := ptr(2049)
	item := h.itemAs("incoming/Blade Runner 2049.HDRip.XviD.AC3-EVO.avi", movieLength,
		store.NewItem{Type: "video", Title: "Blade Runner Xvid", Year: stale})
	got := h.classify(item.ID)
	if got.Title != "Blade Runner 2049" || got.Year != nil {
		t.Fatalf("title %q year %v, want the parse's title and no year", got.Title, got.Year)
	}
	if n := h.tm.hits.Load(); n != 0 {
		t.Errorf("%d TMDB requests; a yearless movie is never asked about", n)
	}
	if got.TMDBID != nil || !got.NeedsCheck {
		t.Errorf("yearless movie matched or unflagged: %+v", got)
	}

	h.tm.add(tmdbMovie{id: 335984, title: "Blade Runner 2049", released: "2017-10-04"}, "Blade Runner 2049|2017")
	dated := h.itemAs("incoming/Blade.Runner.2049.2017.1080p.mkv", movieLength,
		store.NewItem{Type: "video", Title: "Blade Runner", Year: stale})
	if got := h.classify(dated.ID); got.Year == nil || *got.Year != 2017 || got.TMDBID == nil || *got.TMDBID != 335984 {
		t.Errorf("dated copy = %+v, want 2017 matched to 335984", got)
	}

	locked := h.itemAs("incoming/Blade Runner 2049.WEB.avi", movieLength,
		store.NewItem{Type: "movie", Title: "Blade Runner 2049", Year: ptr(2017)})
	yes := true
	if _, err := h.st.UpdateItem(h.ctx, locked.ID, store.UpdateItemParams{Locked: &yes}); err != nil {
		t.Fatal(err)
	}
	if got := h.classify(locked.ID); got.Year == nil || *got.Year != 2017 {
		t.Errorf("locked item's year = %v, want kept 2017", got.Year)
	}

	ep := h.itemAs("Andor/Andor.S01E01.mkv", 2400, store.NewItem{Type: "video", Title: "Andor", Year: ptr(2049)})
	if got := h.classify(ep.ID); got.Type != "episode" || got.Year != nil {
		t.Errorf("episode = %s year %v, want an episode with no stale year", got.Type, got.Year)
	}
}

func TestRuntimeBreaksNamesakeTies(t *testing.T) {
	arrival := tmdbMovie{id: 329865, title: "Arrival", released: "2016-11-10", runtime: 116, overview: "Linguist Louise Banks…"}
	other := tmdbMovie{id: 472349, title: "Arrival", released: "2016-06-01", runtime: 90}
	cases := []struct {
		name    string
		minutes float64
		movies  []tmdbMovie
		want    int64
		details int
	}{
		{"one within 5 %", 113, []tmdbMovie{other, arrival}, 329865, 2},
		{"none within 5 %", 100, []tmdbMovie{other, arrival}, 0, 2},
		{"two within 5 %", 116, []tmdbMovie{arrival, {id: 3, title: "Arrival", released: "2016-01-01", runtime: 118}}, 0, 2},
		{"at most three asked", 116, []tmdbMovie{
			{id: 1, title: "Arrival", released: "2016-01-01", runtime: 70},
			{id: 2, title: "Arrival", released: "2016-02-01", runtime: 80},
			other, arrival,
		}, 0, 3},
		{"unknown runtime never matches", 116, []tmdbMovie{other, {id: 4, title: "Arrival", released: "2016-03-01"}}, 0, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newMovieHarness(t, dummyToken)
			for _, m := range c.movies {
				h.tm.add(m, "Arrival|2016")
			}
			item := h.item("incoming/Arrival.2016.DVDScr.XVID.AC3.HQ.Hive-CM8.avi", c.minutes*60)
			got := h.classify(item.ID)
			var id int64
			if got.TMDBID != nil {
				id = *got.TMDBID
			}
			if id != c.want {
				t.Errorf("tmdb_id = %d, want %d", id, c.want)
			}
			if c.want == 0 && !got.NeedsCheck {
				t.Error("a tie runtime could not break was not flagged")
			}
			if c.want != 0 && (got.Summary == nil || *got.Summary != arrival.overview) {
				t.Errorf("summary = %v, want the matched film's", got.Summary)
			}
			if n := len(h.tm.requests("/3/movie/")); n != c.details {
				t.Errorf("%d detail requests, want %d", n, c.details)
			}
			h.assertTokenHidden()
		})
	}
}

func TestLoneTitleOneYearOffMatches(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(tmdbMovie{id: 1340206, title: "Tuner", released: "2026-01-23"}, "Tuner|2025")
	h.tm.add(tmdbMovie{id: 2, title: "The Piano Tuner", released: "2025-03-01"}, "Tuner|2025")
	item := h.item("incoming/Tuner (2025) 1080p WEBRip.mkv", movieLength)
	if got := h.classify(item.ID); got.TMDBID == nil || *got.TMDBID != 1340206 || got.NeedsCheck {
		t.Errorf("item = %+v, want matched to 1340206", got)
	}

	exact := newMovieHarness(t, dummyToken)
	exact.tm.add(tmdbMovie{id: 10, title: "Crash", released: "2005-05-06"}, "Crash|2004")
	exact.tm.add(tmdbMovie{id: 9, title: "Crash", released: "2004-09-10"}, "Crash|2004")
	crash := exact.item("movies/Crash.2004.mkv", movieLength)
	if got := exact.classify(crash.ID); got.TMDBID == nil || *got.TMDBID != 9 {
		t.Errorf("tmdb_id = %v, want the exact year's 9", got.TMDBID)
	}
}

func TestConfidentMatchClearsParseCheck(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	h.tm.add(tmdbMovie{id: 1307118, title: "Soulm8te", released: "2026-01-09"}, "Soulm8te|2026")
	unsure := h.item("incoming/Soulm8te.2026.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", movieLength)
	if got := h.classify(unsure.ID); got.TMDBID == nil || got.NeedsCheck {
		t.Errorf("item = %+v, want matched with the Check mark cleared", got)
	}
	unmatched := h.item("incoming/Soulm8te.2019.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", movieLength)
	if got := h.classify(unmatched.ID); got.TMDBID != nil || !got.NeedsCheck {
		t.Errorf("unmatched unsure parse = %+v, want flagged", got)
	}
}

func TestMatchAdoptsTMDBTitle(t *testing.T) {
	h := newMovieHarness(t, dummyToken)
	partTwo := tmdbMovie{id: 693134, title: "Dune: Part Two", released: "2024-02-27"}
	h.tm.add(partTwo, "Dune Part Two|2024")
	item := h.item("movies/Dune.Part.Two.2024.2160p.mkv", movieLength)
	if got := h.classify(item.ID); got.Title != "Dune: Part Two" || got.Locked {
		t.Errorf("title %q locked %v, want TMDB's title, unlocked", got.Title, got.Locked)
	}

	amelie := tmdbMovie{id: 194, title: "Amélie", original: "Le Fabuleux Destin d'Amélie Poulain", released: "2001-04-25"}
	h.tm.add(amelie, "Amelie|2001")
	folded := h.item("movies/Amelie.2001.1080p.mkv", movieLength)
	if got := h.classify(folded.ID); got.TMDBID == nil || *got.TMDBID != 194 || got.Title != "Amélie" {
		t.Errorf("Amelie = %+v, want matched to Amélie (194)", got)
	}

	locked := h.item("movies/Dune Part Two (2024).mp4", movieLength)
	yes := true
	if _, err := h.st.UpdateItem(h.ctx, locked.ID, store.UpdateItemParams{Locked: &yes}); err != nil {
		t.Fatal(err)
	}
	if got := h.classify(locked.ID); got.TMDBID == nil || got.Title != "Dune Part Two" {
		t.Errorf("locked item = %+v, want matched keeping its title", got)
	}

	h.tm.add(tmdbMovie{id: 55, title: "Forbidden Fruits", released: "2026-03-01"})
	chosen := h.item("movies/Forbidden.Frutis.2026.mkv", movieLength)
	h.classify(chosen.ID)
	if _, err := h.svc.ChooseMovieMatch(h.ctx, chosen.ID, ptr[int64](55)); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RefreshMovie(h.ctx, chosen.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.get(chosen.ID); got.Title != "Forbidden Fruits" {
		t.Errorf("owner-chosen match title = %q, want TMDB's", got.Title)
	}
	owned := h.item("movies/Dune.Part.Two.2024.WEB.mkv", movieLength)
	h.classify(owned.ID)
	if _, err := h.svc.EditItem(h.ctx, owned.ID, ItemEdit{Title: ptr("Dune 2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ChooseMovieMatch(h.ctx, owned.ID, ptr[int64](55)); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RefreshMovie(h.ctx, owned.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.get(owned.ID); got.Title != "Dune 2" || got.TMDBID == nil || *got.TMDBID != 55 {
		t.Errorf("owner-chosen match on a locked item = %+v, want the owner's title kept", got)
	}
	h.assertTokenHidden()
}

func TestConfidentMovie(t *testing.T) {
	results := []tmdb.Movie{
		{ID: 1, Title: "Dune", Year: 1984},
		{ID: 2, Title: "Dune: Part Two", Year: 2024},
		{ID: 3, Title: "Dune", Year: 2021},
		{ID: 4, Title: "12 Angry Men", Year: 1957},
		{ID: 5, Title: "Twins", Year: 1988},
		{ID: 6, Title: "Twins", Year: 1988},
		{ID: 7, Title: "Tuner", Year: 2026},
		{ID: 8, Title: "The Piano Tuner", Year: 2025},
		{ID: 9, Title: "Crash", Year: 2004},
		{ID: 10, Title: "Crash", Year: 2005},
		{ID: 11, Title: "Heat", Year: 1994},
		{ID: 12, Title: "Heat", Year: 1996},
		{ID: 13, Title: "Amélie", OriginalTitle: "Le Fabuleux Destin d'Amélie Poulain", Year: 2001},
		{ID: 14, Title: "Æon Flux", Year: 2005},
	}
	cases := []struct {
		title string
		year  int
		want  int
		ties  []int
	}{
		{title: "Dune", year: 2021, want: 3},
		{title: "dune", year: 1984, want: 1},
		{title: "Dune", year: 2020, want: 3}, // the lone title a year off
		{title: "Dune", year: 2019},
		{title: "Dune Part Two", year: 2024, want: 2},
		{title: "12 Angry Men", year: 1957, want: 4},
		{title: "Twins", year: 1988, ties: []int{5, 6}},
		{title: "Twins", year: 1989}, // two a year off are not a lone title
		{title: "Tuner", year: 2025, want: 7},
		{title: "Crash", year: 2004, want: 9}, // the exact year beats the one-off
		{title: "Crash", year: 2005, want: 10},
		{title: "Heat", year: 1995},
		{title: "Amelie", year: 2001, want: 13},
		{title: "AMÉLIE", year: 2001, want: 13},
		{title: "Le Fabuleux Destin d'Amelie Poulain", year: 2001, want: 13},
		{title: "AEon Flux", year: 2005, want: 14},
	}
	for _, c := range cases {
		m, ties, ok := confidentMovie(results, c.title, c.year)
		if got := map[bool]int{true: m.ID}[ok]; got != c.want {
			t.Errorf("%q %d → %d, want %d", c.title, c.year, got, c.want)
		}
		var tieIDs []int
		for _, tie := range ties {
			tieIDs = append(tieIDs, tie.ID)
		}
		if !slices.Equal(tieIDs, c.ties) {
			t.Errorf("%q %d ties = %v, want %v", c.title, c.year, tieIDs, c.ties)
		}
	}
}
