package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

// arrival creates an unclassified item at relPath and runs its classify job.
func (e *showsEnv) arrival(relPath string) store.Item {
	e.t.Helper()
	e.seq++
	item, file, err := e.st.CreateItemWithFile(e.ctx, store.NewItem{Type: "video", Title: relPath}, store.NewFile{
		RootID: e.root.ID, RelPath: relPath, Size: 5, Mtime: time.Now(), Fingerprint: "arrival-" + idStr(int64(e.seq)),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpdateFileProbe(e.ctx, file.ID, store.ProbeResult{Container: "mov", DurationS: 2400}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.srv.classifier.Classify(e.ctx, item.ID); err != nil {
		e.t.Fatal(err)
	}
	return e.getItem(item.ID)
}

func (e *showsEnv) showOnWire(id int64) (show struct {
	Title    string `json:"title"`
	TVmazeID *int64 `json:"tvmaze_id"`
	Seasons  []struct {
		Season   int               `json:"season"`
		Episodes []json.RawMessage `json:"episodes"`
	} `json:"seasons"`
}) {
	e.t.Helper()
	e.ok("GET", "/api/shows/"+idStr(id), nil, &show)
	return show
}

// D1 over the wire: an episode the owner moved stays put when show_refresh
// re-matches the outage-formed show it was moved into.
func TestOutageRematchLeavesOwnerMoveAlone(t *testing.T) {
	e := newShowsEnv(t)
	e.tvDown.Store(true)
	first := e.arrival("Andor.S01E01.mkv")
	stray := e.arrival("Andor.Extras.S01E09.mkv")
	outage := *first.ShowID
	e.ok("PATCH", "/api/items/"+idStr(stray.ID), map[string]any{"show_id": outage}, nil)
	local := e.show("Andor (mine)", 0)
	e.exec(`UPDATE shows SET tvmaze_id = 60574 WHERE id = ?`, local.ID)
	e.tvDown.Store(false)

	if err := e.srv.classifier.RefreshShow(e.ctx, outage); err != nil {
		t.Fatal(err)
	}

	if a := e.alias("andor extras"); a.ShowID != outage || a.Source != store.AliasOwner {
		t.Errorf("owner alias = %+v, want it kept on show %d", a, outage)
	}
	if got := e.getItem(stray.ID); got.ShowID == nil || *got.ShowID != outage {
		t.Errorf("owner-moved episode in show %v, want %d", got.ShowID, outage)
	}
	if show := e.showOnWire(outage); show.TVmazeID != nil {
		t.Errorf("owner-touched show re-matched to %d", *show.TVmazeID)
	}
}

// D1 over the wire: after an owner merge, a later confident arrival never
// re-matches the merge target.
func TestOwnerMergeTargetKeepsItsMatch(t *testing.T) {
	e := newShowsEnv(t)
	e.tvDown.Store(true)
	first := e.arrival("Andor.S01E01.mkv")
	target := *e.arrival("Andor.Extras.S01E01.mkv").ShowID
	e.ok("POST", "/api/shows/"+idStr(*first.ShowID)+"/merge", map[string]any{"into_show_id": target}, nil)
	if a := e.alias("andor"); a.ShowID != target || a.Source != store.AliasOwner {
		t.Fatalf("merged alias = %+v, want an owner alias to %d", a, target)
	}
	e.tvDown.Store(false)

	later := e.arrival("Andor.S01E02.mkv")

	if later.ShowID == nil || *later.ShowID != target {
		t.Errorf("later arrival in show %v, want the merge target %d", later.ShowID, target)
	}
	if show := e.showOnWire(target); show.TVmazeID != nil || show.Title != "Andor Extras" {
		t.Errorf("merge target = %q tvmaze %v, want the owner's unmatched %q", show.Title, show.TVmazeID, "Andor Extras")
	}
}

// B17.2 and B17.5 over the wire.
func TestItemPatchLocksOnlyRealEdits(t *testing.T) {
	e := newShowsEnv(t)
	show := e.show("Andor", 0)
	ep := e.episode(show, 1, 1, "Kassa")
	if _, err := e.st.PutShowAlias(e.ctx, "andor", show.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	e.ok("PATCH", "/api/items/"+idStr(ep.ID), map[string]any{"show_id": show.ID}, nil)
	if got := e.getItem(ep.ID); got.Locked {
		t.Errorf("re-sent show_id locked the item")
	}
	if a := e.alias("andor"); a.Source != store.AliasAuto {
		t.Errorf("re-sent show_id taught alias %+v", a)
	}

	movie := e.file(store.NewItem{Type: "movie", Title: "Blade Runner"}, 7000)
	e.ok("PATCH", "/api/items/"+idStr(movie.ID), map[string]any{"year": 1982}, nil)
	if got := e.getItem(movie.ID); !got.Locked || got.Year == nil || *got.Year != 1982 {
		t.Errorf("year edit: locked=%v year=%v, want locked 1982", got.Locked, got.Year)
	}
}

// B17.6 over the wire: item_count is the collection page's card count.
func TestCollectionItemCountIsCardCount(t *testing.T) {
	e := newShowsEnv(t)
	show := e.show("Euphoria", 0)
	coll, err := e.st.CreateCollection(e.ctx, "euphoria")
	if err != nil {
		t.Fatal(err)
	}
	add := func(member map[string]any) {
		if rec := e.do("POST", "/api/collections/"+idStr(coll.ID)+"/items", member); rec.Code != 204 {
			t.Fatalf("add %v = %d %s", member, rec.Code, rec.Body.String())
		}
	}
	for n := 1; n <= 3; n++ {
		add(map[string]any{"item_id": e.episode(show, 1, n, "Ep").ID})
	}
	add(map[string]any{"show_id": show.ID})
	add(map[string]any{"item_id": e.item("movie", "Zodiac", 2007).ID})

	var colls []collectionResponse
	e.ok("GET", "/api/collections", nil, &colls)
	lib := e.library("collection_id=" + idStr(coll.ID))
	if len(colls) != 1 || colls[0].ItemCount != 2 || lib.Total != 2 {
		t.Errorf("item_count = %+v, grid total = %d, want 2 and 2", colls, lib.Total)
	}
}

// D2's wire half: the show page groups an episode without numbers first in
// season 1 with null numbers, as web/src/player/nextEpisode.probe.test.ts's
// wireSeasons assumes.
func TestShowDetailPutsNumberlessEpisodeFirstInSeasonOne(t *testing.T) {
	e := newShowsEnv(t)
	show := e.show("Furious", 0)
	e.episode(show, 1, 1, "Pilot")
	moved := e.file(store.NewItem{Type: "video", Title: "Moved in"}, 3000)
	e.classified(moved.ID, store.UpdateItemParams{Type: ptrTo("episode"), ShowID: store.SetTo(show.ID)})

	wire := e.showOnWire(show.ID)
	if len(wire.Seasons) != 1 || wire.Seasons[0].Season != 1 || len(wire.Seasons[0].Episodes) != 2 {
		t.Fatalf("seasons = %+v, want one season 1 with both episodes", wire.Seasons)
	}
	var head struct {
		ID      int64 `json:"id"`
		Season  *int  `json:"season"`
		Episode *int  `json:"episode"`
	}
	if err := json.Unmarshal(wire.Seasons[0].Episodes[0], &head); err != nil {
		t.Fatal(err)
	}
	if head.ID != moved.ID || head.Season != nil || head.Episode != nil {
		t.Errorf("first season 1 entry = %+v, want the numberless item %d with null numbers", head, moved.ID)
	}
}

func ptrTo[T any](v T) *T { return &v }
