package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/semantic"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

type tvShow struct {
	id          int
	name        string
	premiered   string
	summary     string
	network     string
	country     string
	episodes    []tvEpisode
	poster      bool
	backdrop    bool
	backgrounds []tvBackground // served after the backdrop flag's one
	akas        []string
	runtimes    []int // minutes, by index into episodes; missing means unknown
}

type tvBackground struct {
	width int
	main  bool
	dead  bool // its URL 404s
}

type tvEpisode struct {
	season, number int
	title          string
	airdate        string
}

// fakeTVmaze serves the slice of the TVmaze API the classifier uses.
type fakeTVmaze struct {
	srv *httptest.Server

	mu       sync.Mutex
	shows    map[int]tvShow
	search   map[string][]int // query → show ids, best score first
	searches []string
	paths    []string          // every request path, in order
	images   map[string][]byte // image path → body, else a placeholder
	dead     map[string]bool   // image paths that 404
	// maxAge > 0 sends TVmaze's Cache-Control: public, max-age=N.
	maxAge int
	// stalls holds requests for these URIs without an answer until the
	// channel closes, as TVmaze's origin did; stalled reports each one.
	stalls  map[string]chan struct{}
	stalled chan string

	down  atomic.Bool
	hits  atomic.Int64
	delay time.Duration
}

func newFakeTVmaze(t *testing.T) *fakeTVmaze {
	f := &fakeTVmaze{shows: map[int]tvShow{}, search: map[string][]int{}, images: map[string][]byte{}, dead: map[string]bool{},
		stalls: map[string]chan struct{}{}, stalled: make(chan string, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTVmaze) add(show tvShow, queries ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shows[show.id] = show
	for i, bg := range show.backgrounds {
		if bg.dead {
			f.dead[backgroundPath(show.id, i)] = true
		}
	}
	for _, q := range queries {
		f.search[q] = append(f.search[q], show.id)
	}
}

func backgroundPath(showID, i int) string { return fmt.Sprintf("/img/bg-%d-%d.jpg", showID, i) }

// requests counts the requests whose path starts with prefix.
func (f *fakeTVmaze) requests(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

// stall holds requests for uri until the returned func releases them.
func (f *fakeTVmaze) stall(uri string) (release func()) {
	ch := make(chan struct{})
	f.mu.Lock()
	f.stalls[uri] = ch
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.stalls, uri)
			f.mu.Unlock()
			close(ch)
		})
	}
}

func (f *fakeTVmaze) searched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.searches...)
}

func (f *fakeTVmaze) serve(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	f.mu.Lock()
	release, stalled := f.stalls[r.URL.RequestURI()]
	f.mu.Unlock()
	if stalled {
		f.stalled <- r.URL.RequestURI()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.Error(w, "stalled", http.StatusGatewayTimeout)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	if f.maxAge > 0 {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(f.maxAge))
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/search/shows":
		q := r.URL.Query().Get("q")
		f.searches = append(f.searches, q)
		out := []any{}
		for i, id := range f.search[q] {
			out = append(out, map[string]any{"score": float64(10 - i), "show": f.showJSON(f.shows[id])})
		}
		writeJSON(w, out)
	case parts[0] == "img":
		if f.dead[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		if body, ok := f.images[r.URL.Path]; ok {
			w.Write(body)
			return
		}
		w.Write([]byte("image " + r.URL.Path))
	case parts[0] == "shows" && len(parts) >= 2:
		id, _ := strconv.Atoi(parts[1])
		show, ok := f.shows[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch {
		case len(parts) == 2:
			writeJSON(w, f.showJSON(show))
		case parts[2] == "episodes":
			eps := []any{}
			for i, e := range show.episodes {
				ep := map[string]any{"id": id*1000 + i, "name": e.title, "season": e.season, "number": e.number, "airdate": e.airdate}
				if i < len(show.runtimes) {
					ep["runtime"] = show.runtimes[i]
				}
				eps = append(eps, ep)
			}
			writeJSON(w, eps)
		case parts[2] == "akas":
			akas := []any{}
			for _, name := range show.akas {
				akas = append(akas, map[string]any{"name": name, "country": nil})
			}
			writeJSON(w, akas)
		case parts[2] == "images":
			imgs := []any{}
			if show.backdrop {
				imgs = append(imgs, map[string]any{"type": "background", "main": true,
					"resolutions": map[string]any{"original": map[string]any{"url": f.srv.URL + fmt.Sprintf("/img/bg-%d.jpg", id), "width": 1920}}})
			}
			for i, bg := range show.backgrounds {
				imgs = append(imgs, map[string]any{"type": "background", "main": bg.main,
					"resolutions": map[string]any{"original": map[string]any{"url": f.srv.URL + backgroundPath(id, i), "width": bg.width}}})
			}
			writeJSON(w, imgs)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTVmaze) showJSON(s tvShow) map[string]any {
	m := map[string]any{"id": s.id, "name": s.name, "premiered": s.premiered, "summary": s.summary}
	if s.network != "" {
		network := map[string]any{"name": s.network, "country": nil}
		if s.country != "" {
			network["country"] = map[string]any{"code": s.country}
		}
		m["network"] = network
	}
	if s.poster {
		url := f.srv.URL + fmt.Sprintf("/img/poster-%d.jpg", s.id)
		m["image"] = map[string]any{"medium": url, "original": url}
	}
	return m
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

type scheduled struct {
	showID int64
	at     time.Time
}

type fakeScheduler struct {
	mu     sync.Mutex
	runs   []scheduled
	movies []scheduled // item ids
}

func (f *fakeScheduler) EnqueueMovieRefresh(_ context.Context, itemID int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.movies = append(f.movies, scheduled{itemID, at})
	return nil
}

func (f *fakeScheduler) movieRuns() []scheduled {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scheduled(nil), f.movies...)
}

func (f *fakeScheduler) EnqueueShowRefresh(_ context.Context, showID int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, scheduled{showID, at})
	return nil
}

func (f *fakeScheduler) all() []scheduled {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scheduled(nil), f.runs...)
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	svc    *Service
	tv     *fakeTVmaze
	sched  *fakeScheduler
	bus    *events.Bus
	events <-chan events.Event
	root   store.Root
	art    string
	n      int
}

type harnessOpt func(*Options)

// withClock offsets the service's clock, which moves the legacy cutoff.
func withClock(offset time.Duration) harnessOpt {
	return func(o *Options) { o.Now = func() time.Time { return time.Now().Add(offset) } }
}

func withSol(c Semantic) harnessOpt {
	return func(o *Options) { o.Sol = c }
}

func newHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqldb)
	ctx := context.Background()
	root, err := st.UpsertRoot(ctx, "Media", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tv := newFakeTVmaze(t)
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	ch, cancel := bus.Subscribe()
	t.Cleanup(cancel)
	o := Options{
		Store:  st,
		Bus:    bus,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		TVmaze: tvmaze.New(tvmaze.Options{BaseURL: tv.srv.URL, RateLimit: 10000, RateWindow: time.Second}),
		ArtDir: filepath.Join(t.TempDir(), "art"),
	}
	for _, opt := range opts {
		opt(&o)
	}
	svc := New(o)
	sched := &fakeScheduler{}
	svc.SetScheduler(sched)
	return &harness{t: t, ctx: ctx, st: st, svc: svc, tv: tv, sched: sched, bus: bus, events: ch, root: root, art: o.ArtDir}
}

// item ingests relPath the way the probe job does: the creation guess,
// one probed file.
func (h *harness) item(relPath string, durationS float64) store.Item {
	h.t.Helper()
	return h.itemAs(relPath, durationS, Guess(relPath, durationS))
}

// itemAs ingests relPath with explicit stored values, e.g. what the
// pre-shows parser wrote.
func (h *harness) itemAs(relPath string, durationS float64, in store.NewItem) store.Item {
	h.t.Helper()
	h.n++
	item, file, err := h.st.CreateItemWithFile(h.ctx, in, store.NewFile{
		RootID: h.root.ID, RelPath: relPath, Size: 1, Mtime: time.Now(), Fingerprint: fmt.Sprint(h.n),
	})
	if err != nil {
		h.t.Fatalf("create %s: %v", relPath, err)
	}
	if err := h.st.UpdateFileProbe(h.ctx, file.ID, store.ProbeResult{Container: "matroska", DurationS: durationS}); err != nil {
		h.t.Fatal(err)
	}
	return item
}

func (h *harness) classify(id int64) store.Item {
	h.t.Helper()
	if err := h.svc.Classify(h.ctx, id); err != nil {
		h.t.Fatalf("classify %d: %v", id, err)
	}
	h.invariant()
	return h.get(id)
}

func (h *harness) get(id int64) store.Item {
	h.t.Helper()
	item, err := h.st.GetItem(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return item
}

func (h *harness) show(id int64) store.Show {
	h.t.Helper()
	show, err := h.st.GetShow(h.ctx, id)
	if err != nil {
		h.t.Fatalf("show %d: %v", id, err)
	}
	return show
}

func (h *harness) shows() []store.Show {
	h.t.Helper()
	shows, err := h.st.ListShows(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return shows
}

func (h *harness) invariant() {
	h.t.Helper()
	bad, err := h.st.EpisodeInvariantViolations(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	if len(bad) > 0 {
		h.t.Fatalf("episode invariant broken by items %v", bad)
	}
}

func (h *harness) alias(key string) store.ShowAlias {
	h.t.Helper()
	a, err := h.st.GetShowAlias(h.ctx, key)
	if err != nil {
		h.t.Fatalf("alias %q: %v", key, err)
	}
	return a
}

func (h *harness) setting(key, value string) {
	h.t.Helper()
	if err := h.st.SetSetting(h.ctx, key, value); err != nil {
		h.t.Fatal(err)
	}
}

// drain returns the events published so far.
func (h *harness) drain() []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-h.events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func showEvent(evs []events.Event, typ string, showID int64) bool {
	for _, e := range evs {
		if e.Type != typ {
			continue
		}
		if m, ok := e.Payload.(map[string]any); ok && m["id"] == showID {
			return true
		}
	}
	return false
}

func itemEvent(evs []events.Event, itemID int64) bool {
	for _, e := range evs {
		if s, ok := e.Payload.(store.ItemSummary); ok && e.Type == events.ItemUpdated && s.ID == itemID {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ptr[T any](v T) *T { return &v }

// fakeSol drives the real semantic.Classifier through testdata/fake-sol.sh,
// which prints the transcript written here or fails on demand.
type fakeSol struct {
	t     *testing.T
	dir   string
	calls string
}

func newFakeSol(t *testing.T) (*semantic.Classifier, *fakeSol) {
	t.Helper()
	bin, err := filepath.Abs("testdata/fake-sol.sh")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSol{t: t, dir: t.TempDir()}
	f.calls = filepath.Join(f.dir, "calls")
	t.Setenv("FAKE_SOL_CALLS", f.calls)
	t.Setenv("FAKE_SOL_TRANSCRIPT", filepath.Join(f.dir, "transcript.jsonl"))
	t.Setenv("FAKE_SOL_MODE", "answer")
	return &semantic.Classifier{Bin: bin, Timeout: 10 * time.Second}, f
}

func (f *fakeSol) answer(a semantic.Answer) {
	f.t.Helper()
	text, err := json.Marshal(a)
	if err != nil {
		f.t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": string(text)}})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "transcript.jsonl"), append(line, '\n'), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSol) fail() { f.t.Setenv("FAKE_SOL_MODE", "fail") }

func (f *fakeSol) callCount() int {
	raw, err := os.ReadFile(f.calls)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "\n")
}
