package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/classify"
	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

type showsEnv struct {
	t      *testing.T
	ctx    context.Context
	srv    *Server
	st     *store.Store
	sqldb  *sql.DB
	root   store.Root
	tvDown atomic.Bool
	seq    int
}

func newShowsEnv(t *testing.T) *showsEnv {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	e := &showsEnv{t: t, ctx: context.Background(), st: store.New(sqldb), sqldb: sqldb}
	tv := httptest.NewServer(http.HandlerFunc(e.serveTVmaze))
	t.Cleanup(tv.Close)
	client := tvmaze.New(tvmaze.Options{BaseURL: tv.URL})
	bus := events.NewBus()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.srv = NewServer(Options{
		Store:      e.st,
		Bus:        bus,
		Log:        quiet,
		Version:    "test",
		ThumbsDir:  t.TempDir(),
		TVmaze:     client,
		Classifier: classify.New(classify.Options{Store: e.st, Bus: bus, Log: quiet, TVmaze: client, ArtDir: t.TempDir()}),
	})
	if e.root, err = e.st.UpsertRoot(e.ctx, "Media", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *showsEnv) serveTVmaze(w http.ResponseWriter, r *http.Request) {
	if e.tvDown.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/shows/60574/seasons":
		w.Write([]byte(`[{"number": 1}, {"number": 3}, {"number": 2}, {"number": 0}, {"number": null}]`))
		return
	case "/shows/404/seasons":
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != "/search/shows" || r.URL.Query().Get("q") != "andor" {
		w.Write([]byte("[]"))
		return
	}
	w.Write([]byte(`[
		{"score": 0.9, "show": {"id": 60574, "name": "Andor", "premiered": "2022-09-21",
		  "webChannel": {"name": "Disney+"},
		  "image": {"medium": "https://static.tvmaze.com/m.jpg", "original": "https://static.tvmaze.com/o.jpg"}}},
		{"score": 0.4, "show": {"id": 99, "name": "Andor Talks", "premiered": ""}}
	]`))
}

func (e *showsEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.sqldb.Exec(query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func (e *showsEnv) show(title string, year int) store.Show {
	e.t.Helper()
	in := store.NewShow{Title: title}
	if year > 0 {
		in.Year = &year
	}
	show, err := e.st.CreateShow(e.ctx, in)
	if err != nil {
		e.t.Fatal(err)
	}
	return show
}

// file creates an item with one online, probed file that exists on disk.
func (e *showsEnv) file(in store.NewItem, durationS float64) store.Item {
	e.t.Helper()
	e.seq++
	rel := fmt.Sprintf("media/%d-%s.mkv", e.seq, strings.ReplaceAll(in.Title, " ", "."))
	abs := filepath.Join(e.root.Path, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("video"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	item, file, err := e.st.CreateItemWithFile(e.ctx, in, store.NewFile{
		RootID: e.root.ID, RelPath: rel, Size: 5, Mtime: time.Now(), Fingerprint: strconv.Itoa(e.seq),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpdateFileProbe(e.ctx, file.ID, store.ProbeResult{Container: "mov", DurationS: durationS}); err != nil {
		e.t.Fatal(err)
	}
	return item
}

func (e *showsEnv) item(typ, title string, year int) store.Item {
	e.t.Helper()
	in := store.NewItem{Type: typ, Title: title}
	if year > 0 {
		in.Year = &year
	}
	item := e.file(in, 6000)
	if typ != "episode" {
		e.classified(item.ID, store.UpdateItemParams{})
	}
	return item
}

func (e *showsEnv) episode(show store.Show, season, number int, title string) store.Item {
	e.t.Helper()
	key := showKeyOf(show.Title)
	item := e.file(store.NewItem{Type: "episode", Title: title, ShowKey: &key, Season: &season, Episode: &number}, 3000)
	return e.classified(item.ID, store.UpdateItemParams{ShowID: store.SetTo(show.ID)})
}

func showKeyOf(title string) string { return strings.ToLower(title) }

func (e *showsEnv) classified(id int64, p store.UpdateItemParams) store.Item {
	e.t.Helper()
	p.ClassifiedAt = store.SetTo(store.FormatTime(time.Now()))
	item, err := e.st.UpdateItem(e.ctx, id, p)
	if err != nil {
		e.t.Fatal(err)
	}
	return item
}

func (e *showsEnv) added(item store.Item, at string) {
	e.exec(`UPDATE media_items SET created_at = ? WHERE id = ?`, at, item.ID)
}

func (e *showsEnv) watched(item store.Item, position float64, completed bool, at string) {
	e.exec(`INSERT INTO watch_progress (item_id, position_s, duration_s, completed, updated_at) VALUES (?, ?, 3000, ?, ?)`,
		item.ID, position, completed, at)
}

func (e *showsEnv) do(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func (e *showsEnv) ok(method, path string, body any, out any) {
	e.t.Helper()
	rec := e.do(method, path, body)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("%s %s = %d %s", method, path, rec.Code, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("decode %s: %v", path, err)
		}
	}
}

func (e *showsEnv) fails(method, path string, body any, status int, code string) map[string]any {
	e.t.Helper()
	rec := e.do(method, path, body)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	errBody, _ := got["error"].(map[string]any)
	if rec.Code != status || errBody["code"] != code {
		e.t.Fatalf("%s %s = %d %s, want %d %s", method, path, rec.Code, rec.Body.String(), status, code)
	}
	return got
}

func idStr(n int64) string { return strconv.FormatInt(n, 10) }

type wireEntry struct {
	Kind          string          `json:"kind"`
	ID            int64           `json:"id"`
	Type          string          `json:"type"`
	Title         string          `json:"title"`
	ThumbURL      *string         `json:"thumb_url"`
	PosterURL     *string         `json:"poster_url"`
	BackdropURL   *string         `json:"backdrop_url"`
	HasProgress   bool            `json:"has_progress"`
	EpisodeCount  int             `json:"episode_count"`
	SeasonCount   int             `json:"season_count"`
	CollectionIDs []int64         `json:"collection_ids"`
	UpNext        *upNextResponse `json:"up_next"`
}

type wireLibrary struct {
	Total   int         `json:"total"`
	Entries []wireEntry `json:"entries"`
}

func (e *showsEnv) library(query string) wireLibrary {
	e.t.Helper()
	var lib wireLibrary
	e.ok("GET", "/api/library?"+query, nil, &lib)
	return lib
}

func (e *showsEnv) expectLibrary(query string, total int, want ...string) wireLibrary {
	e.t.Helper()
	lib := e.library(query)
	got := make([]string, len(lib.Entries))
	for i, en := range lib.Entries {
		got[i] = en.Title
		if en.Kind == "show" {
			got[i] = "show:" + en.Title
		}
	}
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) || lib.Total != total {
		e.t.Fatalf("library?%s = %v (total %d), want %v (total %d)", query, got, lib.Total, want, total)
	}
	return lib
}

type libraryFixture struct {
	*showsEnv
	andor, below          store.Show
	andorEps, belowEps    []store.Item
	zodiac, clip, pending store.Item
	toWatch               store.Collection
}

func newLibraryFixture(t *testing.T) *libraryFixture {
	f := &libraryFixture{showsEnv: newShowsEnv(t)}
	f.andor = f.show("Andor", 2022)
	f.below = f.show("Below", 2025)
	for i, title := range []string{"Kassa", "That Would Be Me", "Reckoning"} {
		f.andorEps = append(f.andorEps, f.episode(f.andor, 1, i+1, title))
	}
	f.added(f.andorEps[0], "2026-01-01 00:00:00")
	f.added(f.andorEps[1], "2026-01-02 00:00:00")
	f.added(f.andorEps[2], "2026-03-01 00:00:00")
	for i := range 2 {
		ep := f.episode(f.below, 1, i+1, fmt.Sprintf("Below E%d", i+1))
		f.added(ep, "2026-02-01 00:00:00")
		f.belowEps = append(f.belowEps, ep)
	}
	f.zodiac = f.item("movie", "Zodiac", 2007)
	f.added(f.zodiac, "2026-02-15 00:00:00")
	f.clip = f.item("video", "clip", 0)
	f.added(f.clip, "2026-01-15 00:00:00")
	f.pending = f.item("episode", "Silo S01E01", 0)
	f.added(f.pending, "2026-01-20 00:00:00")

	f.watched(f.andorEps[0], 3000, true, "2026-05-01 00:00:00")
	f.watched(f.zodiac, 600, false, "2026-05-03 00:00:00")

	var err error
	if f.toWatch, err = f.st.CreateCollection(f.ctx, "to watch"); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLibraryUnionSortsAndPages(t *testing.T) {
	f := newLibraryFixture(t)

	lib := f.expectLibrary("", 5, "show:Andor", "Zodiac", "show:Below", "Silo S01E01", "clip")
	if lib.Entries[3].Kind != "item" || lib.Entries[3].Type != "episode" {
		t.Fatalf("pending episode entry = %+v, want an item card of type episode (B1)", lib.Entries[3])
	}
	f.expectLibrary("limit=2", 5, "show:Andor", "Zodiac")
	f.expectLibrary("limit=2&offset=2", 5, "show:Below", "Silo S01E01")
	f.expectLibrary("limit=2&offset=4", 5, "clip")
	f.expectLibrary("sort=added&order=asc", 5, "clip", "Silo S01E01", "show:Below", "Zodiac", "show:Andor")
	f.expectLibrary("sort=title", 5, "show:Andor", "show:Below", "clip", "Silo S01E01", "Zodiac")
	f.expectLibrary("sort=title&order=desc", 5, "Zodiac", "Silo S01E01", "clip", "show:Below", "show:Andor")
	f.expectLibrary("sort=year", 5, "clip", "Silo S01E01", "Zodiac", "show:Andor", "show:Below")
	if got := f.library("sort=watched").Entries; got[0].Title != "Zodiac" || got[1].Title != "Andor" {
		t.Fatalf("sort=watched leads with %q, %q; want Zodiac, Andor", got[0].Title, got[1].Title)
	}

	f.expectLibrary("type=show", 2, "show:Andor", "show:Below")
	f.expectLibrary("type=movie", 1, "Zodiac")
	f.fails("GET", "/api/library?type=episode", nil, 400, "bad_request")
	f.fails("GET", "/api/library?sort=size", nil, 400, "bad_request")

	lib = f.expectLibrary("in_progress=1&sort=watched", 2, "Zodiac", "show:Andor")
	andor := lib.Entries[1]
	if !andor.HasProgress || andor.UpNext == nil || andor.UpNext.ItemID != f.andorEps[1].ID ||
		andor.UpNext.Title != "That Would Be Me" || !andor.UpNext.Available || andor.UpNext.Episode != 2 ||
		andor.UpNext.DurationS == nil || *andor.UpNext.DurationS != 3000 {
		t.Fatalf("continue-watching show = %+v up_next %+v", andor, andor.UpNext)
	}

	f.expectLibrary("q=reckon", 1, "show:Andor")
	f.expectLibrary("q=zod", 1, "Zodiac")
	f.expectLibrary("q=below", 1, "show:Below")
}

func TestLibraryCollectionsHoldShows(t *testing.T) {
	f := newLibraryFixture(t)
	coll := "/api/collections/" + idStr(f.toWatch.ID)
	for _, body := range []map[string]int64{
		{"item_id": f.andorEps[1].ID},
		{"item_id": f.zodiac.ID},
		{"show_id": f.below.ID},
	} {
		if rec := f.do("POST", coll+"/items", body); rec.Code != http.StatusNoContent {
			t.Fatalf("add %v = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	f.fails("POST", coll+"/items", map[string]int64{"item_id": f.clip.ID, "show_id": f.below.ID}, 400, "bad_request")
	f.fails("POST", coll+"/items", map[string]int64{"show_id": 999}, 404, "not_found")

	lib := f.expectLibrary("collection_id="+idStr(f.toWatch.ID), 3, "show:Andor", "Zodiac", "show:Below")
	for _, en := range []wireEntry{lib.Entries[0], lib.Entries[2]} {
		if !reflect.DeepEqual(en.CollectionIDs, []int64{f.toWatch.ID}) {
			t.Fatalf("%s collection_ids = %v", en.Title, en.CollectionIDs)
		}
	}
	f.expectLibrary("uncollected=1", 2, "Silo S01E01", "clip")
	f.expectLibrary("collection_id="+idStr(f.toWatch.ID)+"&type=show", 2, "show:Andor", "show:Below")

	f.fails("PUT", coll+"/order", map[string]any{"item_ids": []int64{f.zodiac.ID}, "entries": []any{}}, 400, "bad_request")
	f.fails("PUT", coll+"/order", map[string]any{"entries": []map[string]int64{{"item_id": 1, "show_id": 1}}}, 400, "bad_request")
	f.fails("PUT", coll+"/order", map[string]any{"item_ids": []int64{f.zodiac.ID, f.andorEps[1].ID}}, 400, "bad_request")
	f.fails("PUT", coll+"/order", map[string]any{"entries": []map[string]int64{
		{"show_id": f.andor.ID}, {"item_id": f.andorEps[1].ID}, {"item_id": f.zodiac.ID}, {"show_id": f.below.ID},
	}}, 400, "bad_request")
	if rec := f.do("PUT", coll+"/order", map[string]any{"entries": []map[string]int64{
		{"show_id": f.below.ID}, {"item_id": f.zodiac.ID}, {"show_id": f.andor.ID},
	}}); rec.Code != http.StatusNoContent {
		t.Fatalf("mixed reorder = %d %s", rec.Code, rec.Body.String())
	}
	f.expectLibrary("collection_id="+idStr(f.toWatch.ID), 3, "show:Below", "Zodiac", "show:Andor")

	// Below is a member by its own row and by an episode's row; removing the
	// show takes both (B3).
	if rec := f.do("POST", coll+"/items", map[string]int64{"item_id": f.belowEps[0].ID}); rec.Code != http.StatusNoContent {
		t.Fatalf("add Below episode = %d", rec.Code)
	}
	if rec := f.do("DELETE", coll+"/shows/"+idStr(f.below.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove show = %d %s", rec.Code, rec.Body.String())
	}
	f.expectLibrary("collection_id="+idStr(f.toWatch.ID), 2, "Zodiac", "show:Andor")
	f.fails("DELETE", coll+"/shows/"+idStr(f.below.ID), nil, 404, "not_found")

	// Andor is a member only through an episode, and removal still works.
	if rec := f.do("DELETE", coll+"/shows/"+idStr(f.andor.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove episode-built show = %d %s", rec.Code, rec.Body.String())
	}
	f.expectLibrary("collection_id="+idStr(f.toWatch.ID), 1, "Zodiac")
	if rec := f.do("PUT", coll+"/order", map[string]any{"item_ids": []int64{f.zodiac.ID}}); rec.Code != http.StatusNoContent {
		t.Fatalf("legacy reorder = %d %s", rec.Code, rec.Body.String())
	}
}

func TestShowCardImages(t *testing.T) {
	f := newLibraryFixture(t)
	andorThumb := "/api/items/" + idStr(f.andorEps[1].ID) + "/thumb"
	show := f.library("type=show&sort=title").Entries[0]
	if show.PosterURL != nil || show.BackdropURL != nil || show.ThumbURL == nil || *show.ThumbURL != andorThumb {
		t.Fatalf("uncached show images = poster %v backdrop %v thumb %v, want thumb %s", show.PosterURL, show.BackdropURL, show.ThumbURL, andorThumb)
	}
	f.fails("GET", "/api/shows/"+idStr(f.andor.ID)+"/poster", nil, 404, "not_found")
	f.fails("GET", "/api/shows/"+idStr(f.andor.ID)+"/backdrop", nil, 404, "not_found")

	for _, p := range []string{f.srv.classifier.PosterPath(f.andor.ID), f.srv.classifier.BackdropPath(f.andor.ID)} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	show = f.library("type=show&sort=title").Entries[0]
	prefix := "/api/shows/" + idStr(f.andor.ID)
	if show.PosterURL == nil || !strings.HasPrefix(*show.PosterURL, prefix+"/poster?v=") ||
		show.BackdropURL == nil || !strings.HasPrefix(*show.BackdropURL, prefix+"/backdrop?v=") ||
		show.ThumbURL == nil || *show.ThumbURL != *show.PosterURL || show.UpNext.ThumbURL != andorThumb {
		t.Fatalf("cached show images = %+v up_next %+v", show, show.UpNext)
	}
	for path, cache := range map[string]string{
		prefix + "/poster":       "no-cache",
		*show.PosterURL:          "public, max-age=31536000, immutable",
		prefix + "/backdrop?v=1": "public, max-age=31536000, immutable",
	} {
		rec := f.do("GET", path, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("Cache-Control") != cache || rec.Body.String() != "jpeg" {
			t.Fatalf("GET %s = %d %v", path, rec.Code, rec.Header())
		}
	}
}

func TestShowDetailSeasons(t *testing.T) {
	e := newShowsEnv(t)
	furious := e.show("Furious", 2026)
	e1 := e.episode(furious, 1, 1, "Pilot")
	e2 := e.episode(furious, 1, 2, "Double")
	end := 3
	e.classified(e2.ID, store.UpdateItemParams{EpisodeEnd: store.SetTo(end)})
	future := time.Now().AddDate(0, 0, 10).Format("2006-01-02")
	aired, special := "2026-08-03", "2026-01-01"
	if err := e.st.ReplaceShowEpisodes(e.ctx, furious.ID, []store.ShowEpisode{
		{Season: 0, Number: 1, Title: "Behind the scenes", Airdate: &special},
		{Season: 1, Number: 1, Title: "Pilot"}, {Season: 1, Number: 2, Title: "Double"}, {Season: 1, Number: 3, Title: "Double II"},
		{Season: 1, Number: 4, Title: "Gone", Airdate: &aired},
		{Season: 1, Number: 5, Title: "Soon", Airdate: &future},
		{Season: 2, Number: 1, Title: "Return"}, {Season: 2, Number: 2, Title: "Again"},
	}); err != nil {
		t.Fatal(err)
	}

	var raw struct {
		showDetailResponse
		Seasons []struct {
			Season   int               `json:"season"`
			Episodes []json.RawMessage `json:"episodes"`
		} `json:"seasons"`
	}
	e.ok("GET", "/api/shows/"+idStr(furious.ID), nil, &raw)
	if raw.Title != "Furious" || raw.EpisodeCount != 2 || raw.SeasonCount != 1 || raw.TVmazeID != nil || raw.UpNext == nil || raw.UpNext.ItemID != e1.ID {
		t.Fatalf("detail summary = %+v", raw.showDetailResponse)
	}
	var got []string
	for _, season := range raw.Seasons {
		for _, ep := range season.Episodes {
			var en struct {
				Kind    string  `json:"kind"`
				ID      int64   `json:"id"`
				Number  int     `json:"number"`
				Season  int     `json:"season"`
				Title   string  `json:"title"`
				Airdate *string `json:"airdate"`
				Missing bool    `json:"missing"`
				Show    *showRefResponse
			}
			if err := json.Unmarshal(ep, &en); err != nil {
				t.Fatal(err)
			}
			switch {
			case en.Kind == "item" && !en.Missing && en.Show != nil && en.Show.ID == furious.ID:
				got = append(got, fmt.Sprintf("s%d item %d", season.Season, en.ID))
			case en.Kind == "missing" && en.Missing && en.Season == season.Season:
				date := "-"
				if en.Airdate != nil {
					date = *en.Airdate
				}
				got = append(got, fmt.Sprintf("s%d missing e%d %s %s", season.Season, en.Number, en.Title, date))
			default:
				t.Fatalf("episode entry %s", ep)
			}
		}
	}
	want := []string{
		fmt.Sprintf("s1 item %d", e1.ID),
		fmt.Sprintf("s1 item %d", e2.ID),
		"s1 missing e4 Gone " + aired,
		"s1 missing e5 Soon " + future,
		"s2 missing e1 Return -",
		"s2 missing e2 Again -",
		"s0 missing e1 Behind the scenes " + special,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("seasons =\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	e.fails("GET", "/api/shows/999", nil, 404, "not_found")
}

func TestShowDetailAppliesSeasonOffset(t *testing.T) {
	e := newShowsEnv(t)
	monsters := e.show("Monsters", 0)
	ep := e.episode(monsters, 2, 1, "Lyle")
	offset := -1
	if _, err := e.st.UpdateShow(e.ctx, monsters.ID, store.UpdateShowParams{SeasonOffset: &offset}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ReplaceShowEpisodes(e.ctx, monsters.ID, []store.ShowEpisode{
		{Season: 1, Number: 1, Title: "Lyle"}, {Season: 1, Number: 2, Title: "Erik"}, {Season: 2, Number: 1, Title: "Ed"},
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := e.srv.showDetail(httptest.NewRequest("GET", "/", nil), monsters.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Seasons) != 2 || detail.Seasons[0].Season != 2 || detail.Seasons[1].Season != 3 || detail.SeasonOffset != -1 {
		t.Fatalf("seasons = %+v", detail.Seasons)
	}
	s2 := detail.Seasons[0].Episodes
	if len(s2) != 2 || s2[0].(itemEntry).ID != ep.ID {
		t.Fatalf("season 2 = %+v", s2)
	}
	if m := s2[1].(missingEpisodeResponse); m.Season != 2 || m.Number != 2 || m.Title != "Erik" {
		t.Fatalf("season 2 missing = %+v", m)
	}
	if m := detail.Seasons[1].Episodes[0].(missingEpisodeResponse); m.Season != 3 || m.Number != 1 {
		t.Fatalf("season 3 = %+v", m)
	}
}

func (e *showsEnv) alias(key string) store.ShowAlias {
	e.t.Helper()
	a, err := e.st.GetShowAlias(e.ctx, key)
	if err != nil {
		e.t.Fatalf("alias %q: %v", key, err)
	}
	return a
}

func (e *showsEnv) aliases(showID int64) []store.ShowAlias {
	e.t.Helper()
	a, err := e.st.ListShowAliases(e.ctx, showID)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *showsEnv) getItem(itemID int64) store.Item {
	e.t.Helper()
	item, err := e.st.GetItem(e.ctx, itemID)
	if err != nil {
		e.t.Fatal(err)
	}
	return item
}

func (e *showsEnv) getShow(showID int64) store.Show {
	e.t.Helper()
	show, err := e.st.GetShow(e.ctx, showID)
	if err != nil {
		e.t.Fatal(err)
	}
	return show
}

func TestItemEditsTeachAliases(t *testing.T) {
	e := newShowsEnv(t)
	andor := e.show("Andor", 2022)
	stray := e.show("Star Wars Andor", 0)
	moved := e.episode(stray, 1, 4, "Aldhani")
	kept := e.episode(andor, 1, 1, "Kassa")
	if _, err := e.st.PutShowAlias(e.ctx, "star wars andor", stray.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}

	var detail itemDetailResponse
	e.ok("PATCH", "/api/items/"+idStr(moved.ID), map[string]any{"show_id": andor.ID}, &detail)
	if detail.Show == nil || detail.Show.ID != andor.ID || detail.Type != "episode" || detail.NeedsCheck {
		t.Fatalf("moved item = %+v", detail)
	}
	if a := e.alias("star wars andor"); a.ShowID != andor.ID || a.Source != store.AliasOwner {
		t.Fatalf("move wrote alias %+v, want owner alias to Andor", a)
	}
	if !e.getItem(moved.ID).Locked {
		t.Fatal("moved item not locked")
	}
	if _, err := e.st.GetShow(e.ctx, stray.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("emptied show survived: %v", err)
	}

	e.classified(kept.ID, store.UpdateItemParams{NeedsCheck: ptr(true)})
	e.ok("PATCH", "/api/items/"+idStr(kept.ID), map[string]any{"confirmed": true}, &detail)
	if detail.NeedsCheck || e.getItem(kept.ID).Locked {
		t.Fatalf("looks right on an item = needs_check %v locked %v; want cleared, unlocked", detail.NeedsCheck, e.getItem(kept.ID).Locked)
	}
	e.ok("PATCH", "/api/items/"+idStr(kept.ID), map[string]any{"season": 1, "episode": 2, "episode_end": 3}, &detail)
	if *detail.Episode != 2 || *detail.EpisodeEnd != 3 || !e.getItem(kept.ID).Locked {
		t.Fatalf("renumbered item = %+v locked %v", detail.episodeFields, e.getItem(kept.ID).Locked)
	}
	e.ok("PATCH", "/api/items/"+idStr(kept.ID), `{"episode_end": null}`, &detail)
	if detail.EpisodeEnd != nil {
		t.Fatalf("episode_end null left %v", *detail.EpisodeEnd)
	}

	movie := e.item("movie", "Zodiac", 2007)
	e.ok("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{"title": "Zodiac (Director's Cut)"}, &detail)
	if got := e.getItem(movie.ID); !got.Locked || got.NeedsCheck || detail.Title != "Zodiac (Director's Cut)" {
		t.Fatalf("title edit = %+v", got)
	}
	e.fails("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{"type": "episode"}, 400, "invalid_episode")
	e.fails("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{"type": "film"}, 400, "bad_request")
	e.fails("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{}, 400, "bad_request")
	e.fails("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{"show_id": 999}, 400, "bad_request")
	e.fails("PATCH", "/api/items/999", map[string]any{"title": "x"}, 404, "not_found")

	e.ok("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{"show_id": andor.ID, "season": 1, "episode": 7}, &detail)
	if detail.Type != "episode" || detail.Show == nil || detail.Show.ID != andor.ID {
		t.Fatalf("movie moved into a show = %+v", detail)
	}
	e.ok("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{"type": "movie"}, &detail)
	if detail.Show != nil || detail.Season != nil || detail.Episode != nil {
		t.Fatalf("leaving episode kept show fields %+v", detail.episodeFields)
	}
}

func ptr[T any](v T) *T { return &v }

func TestShowEditsTeachAliases(t *testing.T) {
	e := newShowsEnv(t)
	andor := e.show("Andor", 2022)
	e.episode(andor, 1, 1, "Kassa")
	if _, err := e.st.PutShowAlias(e.ctx, "andor", andor.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	dupe := e.show("Andor 2022", 0)
	e.episode(dupe, 1, 2, "That Would Be Me")
	if _, err := e.st.PutShowAlias(e.ctx, "andor 2022", dupe.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	tvID := int64(777)
	other, err := e.st.CreateShow(e.ctx, store.NewShow{Title: "Taken", TVmazeID: &tvID})
	if err != nil {
		t.Fatal(err)
	}
	before := e.aliases(andor.ID)

	var detail showDetailResponse
	e.ok("PATCH", "/api/shows/"+idStr(andor.ID), map[string]any{"title": "Andor: A Star Wars Story"}, &detail)
	if got := e.getShow(andor.ID); !got.TitleLocked || got.Title != "Andor: A Star Wars Story" || !detail.TitleLocked {
		t.Fatalf("rename = %+v", got)
	}
	if !reflect.DeepEqual(e.aliases(andor.ID), before) {
		t.Fatal("rename changed aliases")
	}
	e.ok("PATCH", "/api/shows/"+idStr(andor.ID), map[string]any{"confirmed": true}, &detail)
	if detail.NeedsCheck || e.getShow(andor.ID).NeedsCheck {
		t.Fatal("looks right left the Check mark")
	}
	e.fails("PATCH", "/api/shows/"+idStr(andor.ID), map[string]any{}, 400, "bad_request")
	e.fails("PATCH", "/api/shows/"+idStr(andor.ID), map[string]any{"title": " "}, 400, "bad_request")
	e.fails("PATCH", "/api/shows/999", map[string]any{"confirmed": true}, 404, "not_found")

	e.ok("POST", "/api/shows/"+idStr(andor.ID)+"/match", map[string]any{"tvmaze_id": 60574, "season_offset": 0}, &detail)
	if detail.TVmazeID == nil || *detail.TVmazeID != 60574 || detail.NeedsCheck || !reflect.DeepEqual(e.aliases(andor.ID), before) {
		t.Fatalf("match = tvmaze %v needs_check %v aliases %v", detail.TVmazeID, detail.NeedsCheck, e.aliases(andor.ID))
	}
	body := e.fails("POST", "/api/shows/"+idStr(andor.ID)+"/match", map[string]any{"tvmaze_id": tvID}, 409, "match_taken")
	if body["show_id"] != float64(other.ID) {
		t.Fatalf("match_taken body = %v, want show_id %d", body, other.ID)
	}
	e.fails("POST", "/api/shows/"+idStr(andor.ID)+"/match", map[string]any{"season_offset": 1}, 400, "bad_request")
	e.fails("POST", "/api/shows/"+idStr(andor.ID)+"/match", map[string]any{"tvmaze_id": -4}, 400, "bad_request")
	e.ok("POST", "/api/shows/"+idStr(dupe.ID)+"/match", `{"tvmaze_id": null}`, &detail)
	if detail.TVmazeID != nil || detail.NeedsCheck {
		t.Fatalf("not on TVmaze = %+v", detail)
	}

	e.ok("POST", "/api/shows/"+idStr(dupe.ID)+"/merge", map[string]any{"into_show_id": andor.ID}, &detail)
	if detail.ID != andor.ID || detail.EpisodeCount != 2 {
		t.Fatalf("merge returned %+v", detail.showSummaryResponse)
	}
	if a := e.alias("andor 2022"); a.ShowID != andor.ID || a.Source != store.AliasOwner {
		t.Fatalf("merge left alias %+v, want it repointed at Andor as an owner alias (ruling B17)", a)
	}
	e.fails("GET", "/api/shows/"+idStr(dupe.ID), nil, 404, "not_found")
	e.fails("POST", "/api/shows/"+idStr(andor.ID)+"/merge", map[string]any{"into_show_id": andor.ID}, 400, "bad_request")
	e.fails("POST", "/api/shows/"+idStr(andor.ID)+"/merge", map[string]any{"into_show_id": 999}, 404, "not_found")
	e.fails("POST", "/api/shows/"+idStr(andor.ID)+"/merge", map[string]any{}, 400, "bad_request")
}

func TestMatchSearchProxiesTVmaze(t *testing.T) {
	e := newShowsEnv(t)
	tvID := int64(60574)
	local, err := e.st.CreateShow(e.ctx, store.NewShow{Title: "Andor", TVmazeID: &tvID})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Results []matchCandidateResponse `json:"results"`
	}
	e.ok("GET", "/api/shows/match-search?q=andor", nil, &got)
	year := 2022
	network, poster := "Disney+", "https://static.tvmaze.com/m.jpg"
	want := []matchCandidateResponse{
		{TVmazeID: 60574, Name: "Andor", Year: &year, Network: &network, PosterURL: &poster, LocalShowID: &local.ID},
		{TVmazeID: 99, Name: "Andor Talks"},
	}
	if !reflect.DeepEqual(got.Results, want) {
		t.Fatalf("results = %+v", got.Results)
	}
	e.ok("GET", "/api/shows/match-search?q=nothing", nil, &got)
	if got.Results == nil || len(got.Results) != 0 {
		t.Fatalf("empty search = %#v", got.Results)
	}
	e.fails("GET", "/api/shows/match-search?q=+", nil, 400, "bad_request")
	e.tvDown.Store(true)
	e.fails("GET", "/api/shows/match-search?q=andor", nil, 502, "tvmaze_unavailable")
}

func TestMatchSeasonsProxiesTVmaze(t *testing.T) {
	e := newShowsEnv(t)
	var got struct {
		Seasons []int `json:"seasons"`
	}
	e.ok("GET", "/api/shows/match-seasons?tvmaze_id=60574", nil, &got)
	if !reflect.DeepEqual(got.Seasons, []int{1, 2, 3}) {
		t.Fatalf("seasons = %v", got.Seasons)
	}
	e.ok("GET", "/api/shows/match-seasons?tvmaze_id=7", nil, &got)
	if got.Seasons == nil || len(got.Seasons) != 0 {
		t.Fatalf("no seasons = %#v", got.Seasons)
	}
	for _, bad := range []string{"", "abc", "0", "-3"} {
		e.fails("GET", "/api/shows/match-seasons?tvmaze_id="+bad, nil, 400, "bad_request")
	}
	e.fails("GET", "/api/shows/match-seasons?tvmaze_id=404", nil, 404, "not_found")
	e.tvDown.Store(true)
	e.fails("GET", "/api/shows/match-seasons?tvmaze_id=60574", nil, 502, "tvmaze_unavailable")
}

func TestTrashShowMovesEveryLiveEpisode(t *testing.T) {
	e := newShowsEnv(t)
	andor := e.show("Andor", 2022)
	eps := []store.Item{e.episode(andor, 1, 1, "Kassa"), e.episode(andor, 1, 2, "That Would Be Me")}
	gone := e.episode(andor, 1, 3, "Reckoning")
	if rec := e.do("DELETE", "/api/items/"+idStr(gone.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("trash one episode = %d %s", rec.Code, rec.Body.String())
	}

	broken := e.show("Broken", 0)
	present := e.episode(broken, 1, 1, "Here")
	absent := e.episode(broken, 1, 2, "Gone from disk")
	files, _ := e.st.ListFilesForItem(e.ctx, absent.ID)
	if err := os.Remove(filepath.Join(e.root.Path, files[0].RelPath)); err != nil {
		t.Fatal(err)
	}
	e.fails("POST", "/api/shows/"+idStr(broken.ID)+"/trash", nil, 409, "file_missing")
	presentFiles, _ := e.st.ListFilesForItem(e.ctx, present.ID)
	if _, err := os.Stat(filepath.Join(e.root.Path, presentFiles[0].RelPath)); err != nil {
		t.Fatalf("failed show trash moved a file: %v", err)
	}
	if e.getItem(present.ID).DeletedAt != nil {
		t.Fatal("failed show trash marked an episode")
	}

	var got struct {
		ItemIDs []int64 `json:"item_ids"`
	}
	e.ok("POST", "/api/shows/"+idStr(andor.ID)+"/trash", nil, &got)
	if !reflect.DeepEqual(got.ItemIDs, []int64{eps[0].ID, eps[1].ID}) {
		t.Fatalf("trashed %v", got.ItemIDs)
	}
	for _, ep := range eps {
		if e.getItem(ep.ID).DeletedAt == nil {
			t.Fatalf("episode %d not trashed", ep.ID)
		}
		files, _ := e.st.ListFilesForItem(e.ctx, ep.ID)
		if _, err := os.Stat(trashedFilePath(e.root.Path, files[0])); err != nil {
			t.Fatalf("file not in .trash: %v", err)
		}
	}
	e.expectLibrary("type=show", 1, "show:Broken")
	e.fails("POST", "/api/shows/"+idStr(andor.ID)+"/trash", nil, 409, "already_trashed")
	e.fails("POST", "/api/shows/999/trash", nil, 404, "not_found")
}

func TestProgressCompletedFlag(t *testing.T) {
	e := newShowsEnv(t)
	andor := e.show("Andor", 2022)
	ep := e.episode(andor, 1, 1, "Kassa")
	evs, cancel := e.srv.bus.Subscribe()
	defer cancel()
	path := "/api/items/" + idStr(ep.ID) + "/progress"
	for _, step := range []struct {
		body string
		want bool
	}{
		{`{"position_s": 2700, "duration_s": 3000}`, false},
		{`{"position_s": 2700, "duration_s": 3000, "completed": true}`, true},
		{`{"position_s": 30, "duration_s": 3000}`, false},
		{`{"position_s": 2900, "duration_s": 3000, "completed": false}`, true},
	} {
		var got progressResponse
		e.ok("PUT", path, step.body, &got)
		if got.Completed != step.want {
			t.Fatalf("%s → completed %v, want %v", step.body, got.Completed, step.want)
		}
	}
	e.fails("PUT", path, `{"position_s": 1, "duration_s": 3000, "completed": "yes"}`, 400, "bad_request")
	for {
		select {
		case ev := <-evs:
			if m, ok := ev.Payload.(map[string]any); ok && ev.Type == events.ShowUpdated && m["id"] == andor.ID {
				return
			}
		default:
			t.Fatal("progress on an episode published no show.updated")
		}
	}
}

func TestProgressMovesOnFromEarlierEpisode(t *testing.T) {
	e := newShowsEnv(t)
	eden := e.show("East of Eden", 2026)
	e1, e2 := e.episode(eden, 1, 1, "Brothers"), e.episode(eden, 1, 2, "Cathy")
	e.exec(`INSERT INTO watch_progress (item_id, position_s, duration_s, completed, updated_at) VALUES (?, 2790, 3060, 0, ?)`,
		e1.ID, store.FormatTime(time.Now().Add(-time.Hour)))
	evs, cancel := e.srv.bus.Subscribe()
	defer cancel()
	e.ok("PUT", "/api/items/"+idStr(e2.ID)+"/progress", `{"position_s": 40, "duration_s": 3660}`, nil)
	if got, err := e.st.GetProgress(e.ctx, e1.ID); err != nil || !got.Completed {
		t.Fatalf("E1 progress = %+v, %v; want completed once E2 started", got, err)
	}
	for {
		select {
		case ev := <-evs:
			if s, ok := ev.Payload.(store.ItemSummary); ok && ev.Type == events.ItemUpdated && s.ID == e1.ID {
				return
			}
		default:
			t.Fatal("no item.updated for the episode moved on from")
		}
	}
}

func TestPlayMarkersFromChapters(t *testing.T) {
	e := newShowsEnv(t)
	ctx := e.ctx
	cases := []struct {
		name     string
		chapters []store.Chapter
		want     string
	}{
		{"intro and credits", []store.Chapter{
			{StartS: 0, EndS: 10, Title: "Logo"}, {StartS: 10, EndS: 60, Title: "Intro"},
			{StartS: 60, EndS: 500, Title: "Episode"}, {StartS: 500, EndS: 596.4, Title: "Credits"},
		}, `{"intro":{"start_s":10,"end_s":60},"credits_start_s":500}`},
		{"credits only", []store.Chapter{{StartS: 0, EndS: 470, Title: "Chapter 1"}, {StartS: 470, EndS: 596.4, Title: "End Credits"}},
			`{"credits_start_s":470}`},
		{"offset chapters ignored", []store.Chapter{
			{StartS: 300, EndS: 340, Title: "Intro"}, {StartS: 100, EndS: 400, Title: "Credits"}, {StartS: 700, EndS: 760, Title: "Credits"},
		}, `{}`},
		{"no chapters", nil, `{}`},
	}
	for _, tc := range cases {
		item, file := seedProbedItem(t, ctx, e.st)
		if tc.chapters != nil {
			if err := e.st.ReplaceFileChapters(ctx, file.ID, tc.chapters); err != nil {
				t.Fatal(err)
			}
		}
		rec, _ := postPlay(t, e.srv, item.ID, "")
		var body struct {
			Mode    string          `json:"mode"`
			Markers json.RawMessage `json:"markers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
			t.Fatalf("%s: play = %d %s", tc.name, rec.Code, rec.Body.String())
		}
		if string(body.Markers) != tc.want {
			t.Fatalf("%s: markers = %s, want %s", tc.name, body.Markers, tc.want)
		}
	}
}

type fakeSemantic struct{ available bool }

func (f fakeSemantic) Available() bool { return f.available }

func TestSemanticClassificationSetting(t *testing.T) {
	e := newShowsEnv(t)
	e.srv.semantic = fakeSemantic{available: false}
	var got settingsResponse
	e.ok("GET", "/api/settings", nil, &got)
	if got.SemanticClassification != (semanticSettings{Enabled: false, Available: false}) {
		t.Fatalf("default = %+v", got.SemanticClassification)
	}
	e.ok("PUT", "/api/settings/semantic-classification", map[string]bool{"enabled": true}, &got)
	if got.SemanticClassification != (semanticSettings{Enabled: true, Available: false}) {
		t.Fatalf("enabled while unavailable = %+v", got.SemanticClassification)
	}
	if v, err := e.st.GetSetting(e.ctx, store.SettingSemanticClassification); err != nil || v != "1" {
		t.Fatalf("stored setting = %q, %v", v, err)
	}
	e.srv.semantic = fakeSemantic{available: true}
	e.ok("PUT", "/api/settings/semantic-classification", map[string]bool{"enabled": false}, &got)
	if got.SemanticClassification != (semanticSettings{Enabled: false, Available: true}) {
		t.Fatalf("disabled = %+v", got.SemanticClassification)
	}
	e.fails("PUT", "/api/settings/semantic-classification", map[string]any{}, 400, "bad_request")
	e.fails("PUT", "/api/settings/semantic-classification", map[string]any{"enabled": true, "x": 1}, 400, "bad_request")
}

func TestShowEventsOnTheWire(t *testing.T) {
	e := newShowsEnv(t)
	for _, typ := range []string{events.ShowAdded, events.ShowUpdated, events.ShowRemoved} {
		payload, err := e.srv.ssePayload(events.Event{Type: typ, Payload: map[string]any{"id": int64(7)}})
		if err != nil || !reflect.DeepEqual(payload, map[string]int64{"id": 7}) {
			t.Fatalf("%s payload = %v, %v", typ, payload, err)
		}
	}
}

func TestItemSummaryCarriesShowFields(t *testing.T) {
	e := newShowsEnv(t)
	andor := e.show("Andor", 2022)
	ep := e.episode(andor, 1, 2, "That Would Be Me")
	rec := e.do("GET", "/api/items?type=episode", nil)
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw.Items) != 1 {
		t.Fatalf("items = %s", rec.Body.String())
	}
	got := raw.Items[0]
	if got["id"] != float64(ep.ID) || got["season"] != float64(1) || got["episode"] != float64(2) ||
		got["episode_end"] != nil || got["needs_check"] != false ||
		!reflect.DeepEqual(got["show"], map[string]any{"id": float64(andor.ID), "title": "Andor"}) {
		t.Fatalf("episode summary = %v", got)
	}
}

func TestCollectionReorderSkipsTrashedMembers(t *testing.T) {
	f := newLibraryFixture(t)
	coll := "/api/collections/" + idStr(f.toWatch.ID)
	for _, body := range []map[string]int64{{"item_id": f.zodiac.ID}, {"item_id": f.clip.ID}, {"show_id": f.below.ID}} {
		if rec := f.do("POST", coll+"/items", body); rec.Code != http.StatusNoContent {
			t.Fatalf("add %v = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if err := f.st.MarkItemTrashed(f.ctx, f.zodiac.ID); err != nil {
		t.Fatal(err)
	}
	f.expectLibrary("collection_id="+idStr(f.toWatch.ID), 2, "clip", "show:Below")
	if rec := f.do("PUT", coll+"/order", map[string]any{"entries": []map[string]int64{
		{"show_id": f.below.ID}, {"item_id": f.clip.ID},
	}}); rec.Code != http.StatusNoContent {
		t.Fatalf("reorder of the visible members = %d %s", rec.Code, rec.Body.String())
	}
	f.expectLibrary("collection_id="+idStr(f.toWatch.ID), 2, "show:Below", "clip")
	if err := f.st.MarkItemRestored(f.ctx, f.zodiac.ID); err != nil {
		t.Fatal(err)
	}
	f.expectLibrary("collection_id="+idStr(f.toWatch.ID), 3, "Zodiac", "show:Below", "clip")
}
