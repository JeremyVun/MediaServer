package classify

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
		writeJSON(w, f.movieJSON(m))
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
	h.tm.add(tmdbMovie{id: 2, title: "Forbidden Fruits", released: "2023-05-01", backdrop: "/y.jpg"}, "Forbidden Fruits|2024")
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

func TestConfidentMovie(t *testing.T) {
	results := []tmdb.Movie{
		{ID: 1, Title: "Dune", Year: 1984},
		{ID: 2, Title: "Dune: Part Two", Year: 2024},
		{ID: 3, Title: "Dune", Year: 2021},
		{ID: 4, Title: "12 Angry Men", Year: 1957},
		{ID: 5, Title: "Twins", Year: 1988},
		{ID: 6, Title: "Twins", Year: 1988},
	}
	cases := []struct {
		title string
		year  int
		want  int
	}{
		{"Dune", 2021, 3},
		{"dune", 1984, 1},
		{"Dune", 2020, 0},
		{"Dune Part Two", 2024, 2},
		{"12 Angry Men", 1957, 4},
		{"Twins", 1988, 0}, // two name-and-year matches: ambiguous
	}
	for _, c := range cases {
		m, ok := confidentMovie(results, c.title, c.year)
		if got := map[bool]int{true: m.ID}[ok]; got != c.want {
			t.Errorf("%q %d → %d, want %d", c.title, c.year, got, c.want)
		}
	}
}
