package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/classify"
	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tmdb"
)

const fakeToken = "httpapi-dummy-token-5d1e"

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type moviesEnv struct {
	*showsEnv
	cls    *classify.Service
	logs   *lockedBuffer
	tmDown atomic.Bool
	urls   []string
	mu     sync.Mutex
}

func newMoviesEnv(t *testing.T, token string) *moviesEnv {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	e := &moviesEnv{showsEnv: &showsEnv{t: t, ctx: context.Background(), st: store.New(sqldb), sqldb: sqldb}, logs: &lockedBuffer{}}
	fake := httptest.NewServer(http.HandlerFunc(e.serveTMDB))
	t.Cleanup(fake.Close)
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := tmdb.New(tmdb.Options{BaseURL: fake.URL, ImageBaseURL: "https://image.example/t/p/", Token: token, Log: log})
	bus := events.NewBus()
	e.cls = classify.New(classify.Options{Store: e.st, Bus: bus, Log: log, TMDB: client, ArtDir: t.TempDir()})
	e.srv = NewServer(Options{Store: e.st, Bus: bus, Log: log, Version: "test", ThumbsDir: t.TempDir(), TMDB: client, Classifier: e.cls})
	if e.root, err = e.st.UpsertRoot(e.ctx, "Media", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *moviesEnv) serveTMDB(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.urls = append(e.urls, r.URL.RequestURI())
	e.mu.Unlock()
	if e.tmDown.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/3/search/movie" || r.URL.Query().Get("query") != "dune" {
		w.Write([]byte(`{"results":[]}`))
		return
	}
	w.Write([]byte(`{"results":[
		{"id":438631,"title":"Dune","release_date":"2021-09-15","poster_path":"/d.jpg","backdrop_path":"/b.jpg"},
		{"id":841,"title":"Dune","release_date":"","poster_path":null}
	]}`))
}

func (e *moviesEnv) call(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := e.do(method, path, body)
	if strings.Contains(rec.Body.String(), fakeToken) {
		e.t.Errorf("%s %s response carries the token", method, path)
	}
	return rec
}

func (e *moviesEnv) assertTokenHidden() {
	e.t.Helper()
	if strings.Contains(e.logs.String(), fakeToken) {
		e.t.Error("token in log output")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, u := range e.urls {
		if strings.Contains(u, fakeToken) {
			e.t.Errorf("token in request URL %q", u)
		}
	}
}

func (e *moviesEnv) movie(title string, year int) store.Item {
	e.t.Helper()
	return e.item("movie", title, year)
}

func (e *moviesEnv) cacheArt(item store.Item) {
	e.t.Helper()
	path := e.cls.ArtPath(item.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("backdrop"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.UpdateItem(e.ctx, item.ID, store.UpdateItemParams{
		TMDBID: store.SetTo[int64](438631), ArtAt: store.SetTo(store.FormatTime(time.Now())),
	}); err != nil {
		e.t.Fatal(err)
	}
}

type wireMovie struct {
	ID         int64   `json:"id"`
	ArtURL     *string `json:"art_url"`
	TMDBID     *int64  `json:"tmdb_id"`
	NeedsCheck bool    `json:"needs_check"`
	Summary    *string `json:"summary"`
}

func TestItemWireCarriesTMDBFields(t *testing.T) {
	e := newMoviesEnv(t, fakeToken)
	matched := e.movie("Dune", 2021)
	plain := e.movie("Akira", 1988)
	e.cacheArt(matched)

	var detail wireMovie
	e.ok("GET", "/api/items/"+idStr(matched.ID), nil, &detail)
	if detail.TMDBID == nil || *detail.TMDBID != 438631 || detail.ArtURL == nil ||
		!strings.HasPrefix(*detail.ArtURL, "/api/items/"+idStr(matched.ID)+"/art?v=") {
		t.Fatalf("detail = %+v", detail)
	}
	rec := e.call("GET", "/api/items/"+idStr(plain.ID), nil)
	if !strings.Contains(rec.Body.String(), `"art_url":null`) || !strings.Contains(rec.Body.String(), `"tmdb_id":null`) {
		t.Errorf("unmatched detail lacks null fields: %s", rec.Body.String())
	}

	var lib struct {
		Entries []wireMovie `json:"entries"`
	}
	e.ok("GET", "/api/library?type=movie", nil, &lib)
	byID := map[int64]wireMovie{}
	for _, m := range lib.Entries {
		byID[m.ID] = m
	}
	if m := byID[matched.ID]; m.ArtURL == nil || *m.ArtURL != *detail.ArtURL || m.TMDBID == nil {
		t.Errorf("library entry = %+v", m)
	}
	if m := byID[plain.ID]; m.ArtURL != nil || m.TMDBID != nil {
		t.Errorf("unmatched library entry = %+v", m)
	}
	var list struct {
		Items []wireMovie `json:"items"`
	}
	e.ok("GET", "/api/items?type=movie", nil, &list)
	if len(list.Items) != 2 {
		t.Fatalf("items = %+v", list.Items)
	}

	if err := os.Remove(e.cls.ArtPath(matched.ID)); err != nil {
		t.Fatal(err)
	}
	e.ok("GET", "/api/items/"+idStr(matched.ID), nil, &detail)
	if detail.ArtURL != nil {
		t.Errorf("art_url %q for a missing file", *detail.ArtURL)
	}
}

func TestItemArtEndpoint(t *testing.T) {
	e := newMoviesEnv(t, fakeToken)
	matched := e.movie("Dune", 2021)
	e.cacheArt(matched)

	rec := e.call("GET", "/api/items/"+idStr(matched.ID)+"/art?v=1", nil)
	if rec.Code != 200 || rec.Body.String() != "backdrop" || rec.Header().Get("Content-Type") != "image/jpeg" ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("versioned art = %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := e.call("GET", "/api/items/"+idStr(matched.ID)+"/art", nil); rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("bare art = %d %v", rec.Code, rec.Header())
	}
	plain := e.movie("Akira", 1988)
	e.fails("GET", "/api/items/"+idStr(plain.ID)+"/art", nil, 404, "not_found")
	e.fails("GET", "/api/items/9999/art", nil, 404, "not_found")
}

func TestItemMatchSearch(t *testing.T) {
	e := newMoviesEnv(t, fakeToken)
	item := e.movie("Dune", 2021)
	var got struct {
		Results []map[string]any `json:"results"`
	}
	rec := e.call("GET", "/api/items/"+idStr(item.ID)+"/match-search?q=dune", nil)
	if rec.Code != 200 {
		t.Fatalf("match-search = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"tmdb_id": 438631.0, "title": "Dune", "year": 2021.0, "poster_url": "https://image.example/t/p/w185/d.jpg"},
		{"tmdb_id": 841.0, "title": "Dune", "year": nil, "poster_url": nil},
	}
	if len(got.Results) != 2 {
		t.Fatalf("results = %+v", got.Results)
	}
	for i, w := range want {
		for k, v := range w {
			if got.Results[i][k] != v {
				t.Errorf("result %d %s = %v, want %v", i, k, got.Results[i][k], v)
			}
		}
	}

	e.call("GET", "/api/items/"+idStr(item.ID)+"/match-search?q=dune&year=2021", nil)
	e.mu.Lock()
	last := e.urls[len(e.urls)-1]
	e.mu.Unlock()
	if !strings.Contains(last, "year=2021") {
		t.Errorf("year not forwarded: %s", last)
	}

	e.fails("GET", "/api/items/"+idStr(item.ID)+"/match-search", nil, 400, "bad_request")
	e.fails("GET", "/api/items/"+idStr(item.ID)+"/match-search?q=dune&year=x", nil, 400, "bad_request")
	e.fails("GET", "/api/items/9999/match-search?q=dune", nil, 404, "not_found")
	e.tmDown.Store(true)
	e.fails("GET", "/api/items/"+idStr(item.ID)+"/match-search?q=dune", nil, 502, "tmdb_unavailable")
	e.assertTokenHidden()

	off := newMoviesEnv(t, "")
	offItem := off.movie("Dune", 2021)
	off.fails("GET", "/api/items/"+idStr(offItem.ID)+"/match-search?q=dune", nil, 503, "tmdb_unavailable")
	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.urls) != 0 {
		t.Errorf("requests without a token: %v", off.urls)
	}
}

func TestItemMatch(t *testing.T) {
	e := newMoviesEnv(t, fakeToken)
	item := e.movie("Forbidden Fruits", 2024)
	e.classified(item.ID, store.UpdateItemParams{NeedsCheck: ptr(true)})

	var got wireMovie
	e.ok("POST", "/api/items/"+idStr(item.ID)+"/match", map[string]any{"tmdb_id": 438631}, &got)
	if got.TMDBID == nil || *got.TMDBID != 438631 || got.NeedsCheck {
		t.Fatalf("after match = %+v", got)
	}
	e.cacheArt(item)
	e.ok("POST", "/api/items/"+idStr(item.ID)+"/match", map[string]any{"tmdb_id": nil}, &got)
	if got.TMDBID != nil || got.ArtURL != nil || got.NeedsCheck {
		t.Fatalf("after not on TMDB = %+v", got)
	}
	if after := e.getItem(item.ID); after.TMDBCheckedAt == nil {
		t.Error("not on TMDB left matching unsettled")
	}

	e.fails("POST", "/api/items/"+idStr(item.ID)+"/match", map[string]any{}, 400, "bad_request")
	e.fails("POST", "/api/items/"+idStr(item.ID)+"/match", map[string]any{"tmdb_id": 0}, 400, "bad_request")
	e.fails("POST", "/api/items/"+idStr(item.ID)+"/match", map[string]any{"tmdb": 1}, 400, "bad_request")
	video := e.item("video", "Clip", 0)
	e.fails("POST", "/api/items/"+idStr(video.ID)+"/match", map[string]any{"tmdb_id": 1}, 400, "bad_request")
	e.fails("POST", "/api/items/9999/match", map[string]any{"tmdb_id": nil}, 404, "not_found")

	off := newMoviesEnv(t, "")
	offItem := off.movie("Dune", 2021)
	off.fails("POST", "/api/items/"+idStr(offItem.ID)+"/match", map[string]any{"tmdb_id": 1}, 503, "tmdb_unavailable")
	off.ok("POST", "/api/items/"+idStr(offItem.ID)+"/match", map[string]any{"tmdb_id": nil}, &got)
	e.assertTokenHidden()
}

func TestPurgeDeletesItemArt(t *testing.T) {
	e := newMoviesEnv(t, fakeToken)
	item := e.movie("Dune", 2021)
	e.cacheArt(item)
	if err := e.st.MarkItemTrashed(e.ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if rec := e.call("DELETE", "/api/items/"+idStr(item.ID)+"/purge", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("purge = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(e.cls.ArtPath(item.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("art survived the purge: %v", err)
	}
}

func TestSettingsReportTMDB(t *testing.T) {
	for token, want := range map[string]bool{fakeToken: true, "": false} {
		e := newMoviesEnv(t, token)
		var got struct {
			TMDB struct {
				Configured bool `json:"configured"`
			} `json:"tmdb"`
		}
		e.ok("GET", "/api/settings", nil, &got)
		if got.TMDB.Configured != want {
			t.Errorf("token set %v: configured = %v", token != "", got.TMDB.Configured)
		}
		if rec := e.call("GET", "/api/settings", nil); strings.Contains(rec.Body.String(), "token") {
			t.Errorf("settings mention the token: %s", rec.Body.String())
		}
	}
}
