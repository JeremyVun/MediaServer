package httpapi

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

// probe: every shows-era endpoint answers bad input with the {"error":{"code","message"}} envelope and its stable slug.
func TestProbe_Wire_ShowEndpointsErrorEnvelope(t *testing.T) {
	e := newShowsEnv(t)
	andor := e.show("Andor", 2022)
	e.exec(`UPDATE shows SET tvmaze_id = 60574 WHERE id = ?`, andor.ID)
	other := e.show("Other", 0)
	ep := e.episode(andor, 1, 1, "Kassa")
	movie := e.item("movie", "Zodiac", 2007)
	coll, err := e.st.CreateCollection(e.ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	a, o, epID, mv, c := idStr(andor.ID), idStr(other.ID), idStr(ep.ID), idStr(movie.ID), idStr(coll.ID)

	cases := []struct {
		method, path string
		body         any
		status       int
		code         string
	}{
		{"GET", "/api/shows/abc", nil, 400, "bad_request"},
		{"GET", "/api/shows/999", nil, 404, "not_found"},
		{"PATCH", "/api/shows/999", map[string]any{"confirmed": true}, 404, "not_found"},
		{"PATCH", "/api/shows/" + a, "{", 400, "bad_request"},
		{"PATCH", "/api/shows/" + a, map[string]any{"bogus": 1}, 400, "bad_request"},
		{"PATCH", "/api/shows/" + a, map[string]any{}, 400, "bad_request"},
		{"PATCH", "/api/shows/" + a, map[string]any{"title": "  "}, 400, "bad_request"},
		{"POST", "/api/shows/999/match", map[string]any{"tvmaze_id": 5}, 404, "not_found"},
		{"POST", "/api/shows/" + a + "/match", map[string]any{}, 400, "bad_request"},
		{"POST", "/api/shows/" + a + "/match", map[string]any{"tvmaze_id": 0}, 400, "bad_request"},
		{"POST", "/api/shows/" + a + "/match", map[string]any{"tvmaze_id": -3}, 400, "bad_request"},
		{"POST", "/api/shows/" + a + "/match", map[string]any{"tvmaze_id": "x"}, 400, "bad_request"},
		{"POST", "/api/shows/" + o + "/match", map[string]any{"tvmaze_id": 60574}, 409, "match_taken"},
		{"POST", "/api/shows/" + a + "/merge", map[string]any{"into_show_id": andor.ID}, 400, "bad_request"},
		{"POST", "/api/shows/" + a + "/merge", map[string]any{"into_show_id": 999}, 404, "not_found"},
		{"POST", "/api/shows/999/merge", map[string]any{"into_show_id": andor.ID}, 404, "not_found"},
		{"POST", "/api/shows/" + a + "/merge", map[string]any{}, 400, "bad_request"},
		{"GET", "/api/shows/match-search", nil, 400, "bad_request"},
		{"GET", "/api/shows/match-seasons?tvmaze_id=0", nil, 400, "bad_request"},
		{"GET", "/api/shows/match-seasons?tvmaze_id=404", nil, 404, "not_found"},
		{"GET", "/api/shows/" + a + "/poster", nil, 404, "not_found"},
		{"GET", "/api/shows/999/backdrop", nil, 404, "not_found"},
		{"POST", "/api/shows/999/trash", nil, 404, "not_found"},
		{"POST", "/api/shows/" + o + "/trash", nil, 409, "already_trashed"},
		{"GET", "/api/library?type=episode", nil, 400, "bad_request"},
		{"GET", "/api/library?sort=bogus", nil, 400, "bad_request"},
		{"GET", "/api/library?order=up", nil, 400, "bad_request"},
		{"GET", "/api/library?offset=-1", nil, 400, "bad_request"},
		{"PATCH", "/api/items/" + mv, map[string]any{"type": "episode"}, 400, "invalid_episode"},
		{"PATCH", "/api/items/" + mv, map[string]any{"show_id": 999}, 400, "bad_request"},
		{"PATCH", "/api/items/" + mv, map[string]any{"type": "movie", "show_id": andor.ID}, 400, "bad_request"},
		{"PATCH", "/api/items/" + epID, map[string]any{"episode": 0}, 400, "bad_request"},
		{"PATCH", "/api/items/" + epID, map[string]any{"season": -1}, 400, "bad_request"},
		{"PATCH", "/api/items/" + epID, map[string]any{"episode_end": 1}, 400, "bad_request"},
		{"PATCH", "/api/items/" + mv, map[string]any{"season": 1}, 400, "bad_request"},
		{"PATCH", "/api/items/999", map[string]any{"title": "x"}, 404, "not_found"},
		{"PATCH", "/api/items/" + epID, map[string]any{}, 400, "bad_request"},
		{"PUT", "/api/items/" + epID + "/progress", map[string]any{"position_s": -1, "duration_s": 10}, 400, "bad_request"},
		{"PUT", "/api/items/" + epID + "/progress", map[string]any{"position_s": 1, "duration_s": 10, "completed": "yes"}, 400, "bad_request"},
		{"POST", "/api/collections/" + c + "/items", map[string]any{"item_id": ep.ID, "show_id": andor.ID}, 400, "bad_request"},
		{"POST", "/api/collections/" + c + "/items", map[string]any{"show_id": 999}, 404, "not_found"},
		{"POST", "/api/collections/999/items", map[string]any{"show_id": andor.ID}, 404, "not_found"},
		{"DELETE", "/api/collections/" + c + "/shows/" + o, nil, 404, "not_found"},
		{"PUT", "/api/collections/" + c + "/order", map[string]any{"entries": []any{map[string]any{"item_id": 1, "show_id": 1}}}, 400, "bad_request"},
		{"PUT", "/api/collections/" + c + "/order", map[string]any{"entries": []any{}, "item_ids": []any{}}, 400, "bad_request"},
	}
	for _, tc := range cases {
		rec := e.do(tc.method, tc.path, tc.body)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s %s: %d body %q is not JSON", tc.method, tc.path, rec.Code, rec.Body.String())
			continue
		}
		var envelope struct {
			Code    *string `json:"code"`
			Message *string `json:"message"`
		}
		_ = json.Unmarshal(body["error"], &envelope)
		extra := slices.DeleteFunc(slices.Collect(func(yield func(string) bool) {
			for k := range body {
				if !yield(k) {
					return
				}
			}
		}), func(k string) bool { return k == "error" || (tc.code == "match_taken" && k == "show_id") })
		switch {
		case rec.Code != tc.status || envelope.Code == nil || *envelope.Code != tc.code:
			t.Errorf("%s %s = %d %s, want %d %s", tc.method, tc.path, rec.Code, rec.Body.String(), tc.status, tc.code)
		case envelope.Message == nil || *envelope.Message == "" || len(extra) > 0:
			t.Errorf("%s %s: body %s is not the bare error envelope", tc.method, tc.path, rec.Body.String())
		}
	}
}

// probe: show trash is all or nothing: one episode that can't move (offline root, file gone from disk) leaves every episode live and every file where it was; then a full trash and per-item restores bring every episode back.
func TestProbe_Destroy_ShowTrashAllOrNothing(t *testing.T) {
	e := newShowsEnv(t)
	show := e.show("Split", 0)
	first := e.episode(show, 1, 1, "One")
	second := e.episode(show, 1, 2, "Two")
	livePaths := func() []string {
		var out []string
		for _, id := range []int64{first.ID, second.ID} {
			files, err := e.st.ListFilesForItem(e.ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			root, err := e.st.GetRoot(e.ctx, files[0].RootID)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, filepath.Join(root.Path, files[0].RelPath))
		}
		return out
	}
	assertUntouched := func(step string) {
		t.Helper()
		for _, id := range []int64{first.ID, second.ID} {
			item, err := e.st.GetItem(e.ctx, id)
			if err != nil || item.DeletedAt != nil {
				t.Errorf("%s: item %d trashed (%v)", step, id, err)
			}
		}
	}

	paths := livePaths()
	hidden := paths[1] + ".away"
	if err := os.Rename(paths[1], hidden); err != nil {
		t.Fatal(err)
	}
	e.fails("POST", "/api/shows/"+idStr(show.ID)+"/trash", nil, http.StatusConflict, "file_missing")
	assertUntouched("second file missing")
	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("first episode's file not rolled back to %s: %v", paths[0], err)
	}
	if err := os.Rename(hidden, paths[1]); err != nil {
		t.Fatal(err)
	}

	away, err := e.st.UpsertRoot(e.ctx, "Away", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	third := e.file(store.NewItem{Type: "episode", Title: "Three"}, 3000)
	e.exec(`UPDATE media_files SET root_id = ? WHERE item_id = ?`, away.ID, third.ID)
	e.classified(third.ID, store.UpdateItemParams{ShowID: store.SetTo(show.ID), Season: store.SetTo(1), Episode: store.SetTo(3)})
	if err := e.st.SetRootOnline(e.ctx, away.ID, false); err != nil {
		t.Fatal(err)
	}
	e.fails("POST", "/api/shows/"+idStr(show.ID)+"/trash", nil, http.StatusConflict, "root_offline")
	assertUntouched("third on an offline root")
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s moved by a refused trash: %v", p, err)
		}
	}
	e.exec(`UPDATE media_items SET show_id = NULL, type = 'video' WHERE id = ?`, third.ID)

	var trashed struct {
		ItemIDs []int64 `json:"item_ids"`
	}
	e.ok("POST", "/api/shows/"+idStr(show.ID)+"/trash", nil, &trashed)
	if len(trashed.ItemIDs) != 2 {
		t.Fatalf("trashed %v, want both episodes", trashed.ItemIDs)
	}
	if lib := e.library("type=show"); lib.Total != 0 {
		t.Errorf("trashed show still in the grid: %+v", lib.Entries)
	}
	for _, id := range trashed.ItemIDs {
		e.ok("POST", "/api/items/"+idStr(id)+"/restore", nil, nil)
	}
	var detail struct {
		EpisodeCount int `json:"episode_count"`
	}
	e.ok("GET", "/api/shows/"+idStr(show.ID), nil, &detail)
	if detail.EpisodeCount != 2 {
		t.Errorf("restored show has %d episodes, want 2", detail.EpisodeCount)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not back after restore: %v", p, err)
		}
	}
}

// probe: an unmounted root never removes a show from /api/library or its page; up_next stays the true next episode, unavailable (M10).
func TestProbe_Offline_LibraryAndShowPage(t *testing.T) {
	e := newShowsEnv(t)
	show := e.show("Away", 0)
	e1 := e.episode(show, 1, 1, "One")
	e2 := e.episode(show, 1, 2, "Two")
	e.watched(e1, 3000, true, "2026-01-01 10:00:00")
	if err := e.st.SetRootOnline(e.ctx, e.root.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetRootFilesStatus(e.ctx, e.root.ID, "offline"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"", "type=show", "in_progress=1"} {
		lib := e.library(q)
		found := false
		for _, en := range lib.Entries {
			if en.Kind == "show" && en.ID == show.ID {
				found = true
				if en.UpNext == nil || en.UpNext.ItemID != e2.ID || en.UpNext.Available || en.EpisodeCount != 2 {
					t.Errorf("%q: entry %+v up_next %+v, want E2 unavailable with 2 episodes", q, en, en.UpNext)
				}
			}
		}
		if !found {
			t.Errorf("%q: offline show missing from /api/library", q)
		}
	}
	var detail struct {
		Seasons []struct {
			Episodes []map[string]any `json:"episodes"`
		} `json:"seasons"`
	}
	e.ok("GET", "/api/shows/"+idStr(show.ID), nil, &detail)
	if len(detail.Seasons) != 1 || len(detail.Seasons[0].Episodes) != 2 {
		t.Errorf("show page seasons = %+v, want both episodes while offline", detail.Seasons)
	}
}

func showEvents(evs <-chan events.Event) map[string][]int64 {
	out := map[string][]int64{}
	for {
		select {
		case ev := <-evs:
			if !strings.HasPrefix(ev.Type, "show.") {
				continue
			}
			if id, ok := eventPayloadInt(ev.Payload, "id"); ok {
				out[ev.Type] = append(out[ev.Type], id)
			}
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

// probe: every change to a show's episodes over the wire publishes show.updated (or show.removed) for each show it touches, so clients heal.
func TestProbe_Wire_ShowEventsFollowEpisodeChanges(t *testing.T) {
	e := newShowsEnv(t)
	evs, cancel := e.srv.bus.Subscribe()
	defer cancel()
	a, b := e.show("A", 0), e.show("B", 0)
	a1, a2 := e.episode(a, 1, 1, "A1"), e.episode(a, 1, 2, "A2")
	b1 := e.episode(b, 1, 1, "B1")
	coll, err := e.st.CreateCollection(e.ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	showEvents(evs)

	steps := []struct {
		name    string
		run     func()
		updated []int64
		removed []int64
	}{
		{"detach A2", func() { e.ok("PATCH", "/api/items/"+idStr(a2.ID), map[string]any{"type": "movie"}, nil) }, []int64{a.ID}, nil},
		{"move A2 back into B", func() { e.ok("PATCH", "/api/items/"+idStr(a2.ID), map[string]any{"show_id": b.ID}, nil) }, []int64{b.ID}, nil},
		{"move B1 to A", func() { e.ok("PATCH", "/api/items/"+idStr(b1.ID), map[string]any{"show_id": a.ID}, nil) }, []int64{a.ID, b.ID}, nil},
		{"progress on A1", func() {
			e.ok("PUT", "/api/items/"+idStr(a1.ID)+"/progress", map[string]any{"position_s": 30, "duration_s": 3000}, nil)
		}, []int64{a.ID}, nil},
		{"add A1 to a collection", func() {
			if rec := e.do("POST", "/api/collections/"+idStr(coll.ID)+"/items", map[string]any{"item_id": a1.ID}); rec.Code != 204 {
				t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
			}
		}, []int64{a.ID}, nil},
		{"remove A from the collection", func() {
			if rec := e.do("DELETE", "/api/collections/"+idStr(coll.ID)+"/shows/"+idStr(a.ID), nil); rec.Code != 204 {
				t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
			}
		}, []int64{a.ID}, nil},
		{"trash A1", func() {
			if rec := e.do("DELETE", "/api/items/"+idStr(a1.ID), nil); rec.Code != 204 {
				t.Fatalf("trash: %d %s", rec.Code, rec.Body.String())
			}
		}, []int64{a.ID}, nil},
		{"restore A1", func() { e.ok("POST", "/api/items/"+idStr(a1.ID)+"/restore", nil, nil) }, []int64{a.ID}, nil},
		{"detach B's last episode", func() { e.ok("PATCH", "/api/items/"+idStr(a2.ID), map[string]any{"type": "video"}, nil) }, nil, []int64{b.ID}},
	}
	for _, step := range steps {
		step.run()
		got := showEvents(evs)
		for _, id := range step.updated {
			if !slices.Contains(got[events.ShowUpdated], id) {
				t.Errorf("%s: no show.updated for show %d (got %v)", step.name, id, got)
			}
		}
		for _, id := range step.removed {
			if !slices.Contains(got[events.ShowRemoved], id) {
				t.Errorf("%s: no show.removed for show %d (got %v)", step.name, id, got)
			}
		}
	}
}

// repoRoot is the module root, two levels above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// goFiles parses every non-test Go file under dirs (relative to the repo root).
func goFiles(t *testing.T, dirs ...string) map[string]*ast.File {
	t.Helper()
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(repoRoot(t), dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repoRoot(t), path)
			out[rel] = f
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// probe: no package outside internal/store (and the db opener) imports database/sql (repo hard rule).
func TestProbe_Rules_NoDatabaseSQLOutsideStore(t *testing.T) {
	seen := 0
	for path, f := range goFiles(t, "internal", "cmd") {
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) != "database/sql" {
				continue
			}
			seen++
			if !strings.HasPrefix(path, "internal/store/") && !strings.HasPrefix(path, "internal/db/") {
				t.Errorf("%s imports database/sql", path)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no file imports database/sql; the walk found nothing")
	}
}

// probe: every child process is exec.CommandContext, and the shows-era packages bound each with a timeout (repo hard rule).
func TestProbe_Rules_ChildProcessesHaveContextAndTimeout(t *testing.T) {
	for path, f := range goFiles(t, "internal", "cmd") {
		var usesContext bool
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" {
				switch sel.Sel.Name {
				case "Command":
					t.Errorf("%s: exec.Command without a context", path)
				case "CommandContext":
					usesContext = true
				}
			}
			return true
		})
		shows := strings.HasPrefix(path, "internal/classify/") || strings.HasPrefix(path, "internal/semantic/") ||
			strings.HasPrefix(path, "internal/probe/")
		if usesContext && shows {
			raw, err := os.ReadFile(filepath.Join(repoRoot(t), path))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "WithTimeout") {
				t.Errorf("%s runs a child with no context.WithTimeout in the file", path)
			}
		}
	}
}

// probe: media files are deleted only by the trash purge: every os.Remove/RemoveAll call site is one of these, each removing its own scratch, cache or art.
func TestProbe_Destroy_RemoveCallSitesAreKnown(t *testing.T) {
	known := map[string]string{
		"internal/httpapi/trash.go":      "trash purge (the one media-file delete)",
		"internal/jobs/manager.go":       "trash purge job and stale upload part files",
		"internal/httpapi/uploads.go":    "upload part files",
		"internal/httpapi/settings.go":   "HLS cache dir write probe",
		"internal/classify/shows.go":     "show art of dead or re-matched shows, art temp files",
		"internal/classify/movies.go":    "movie backdrops of purged or retyped items, temp files",
		"internal/classify/art.go":       "art download temp files",
		"internal/tvmaze/client.go":      "image download temp file",
		"internal/tmdb/client.go":        "image download temp file",
		"internal/semantic/semantic.go":  "codex temp working dir",
		"internal/playback/session.go":   "HLS session cache dirs",
		"internal/playback/subtitles.go": "subtitle temp files",
		"internal/playback/keyframes.go": "keyframe temp file",
		"internal/logging/logging.go":    "log rotation",
	}
	for path, f := range goFiles(t, "internal", "cmd") {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" && (sel.Sel.Name == "Remove" || sel.Sel.Name == "RemoveAll") {
				if _, ok := known[path]; !ok {
					t.Errorf("%s calls os.%s: review that it can never delete a media file, then list it here", path, sel.Sel.Name)
				}
			}
			return true
		})
	}
}
