package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/store"
)

type catalog struct {
	*fixture
	andor, below, carrie, dark store.Show
	andorEps, belowEps         []store.Item
	darkEp                     store.Item
	zodiac, clip, pending      store.Item
	toWatch                    store.Collection
}

func (f *fixture) added(item store.Item, at string) {
	f.t.Helper()
	f.exec(`UPDATE media_items SET created_at = ? WHERE id = ?`, at, item.ID)
}

func (f *fixture) progress(item store.Item, position, duration float64, completed bool, at string) {
	f.t.Helper()
	f.exec(`INSERT INTO watch_progress (item_id, position_s, duration_s, completed, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET position_s = excluded.position_s, completed = excluded.completed, updated_at = excluded.updated_at`,
		item.ID, position, duration, completed, at)
}

func newCatalog(t *testing.T) *catalog {
	f := newFixture(t)
	l := &catalog{fixture: f}
	l.andor = f.show("Andor")
	l.below = f.show("Below")
	l.carrie = f.show("Carrie")
	l.dark = f.show("Dark")
	f.exec(`UPDATE shows SET year = 2022, needs_check = 0 WHERE id = ?`, l.andor.ID)
	f.exec(`UPDATE shows SET year = 2025 WHERE id = ?`, l.below.ID)

	for i, title := range []string{"Kassa", "That Would Be Me", "Reckoning"} {
		l.andorEps = append(l.andorEps, f.episode(l.andor, 1, i+1, title))
	}
	f.added(l.andorEps[0], "2026-01-01 00:00:00")
	f.added(l.andorEps[1], "2026-01-02 00:00:00")
	f.added(l.andorEps[2], "2026-03-01 00:00:00")
	for i, title := range []string{"Below E1", "Below E2"} {
		ep := f.episode(l.below, 1, i+1, title)
		f.added(ep, "2026-02-01 00:00:00")
		l.belowEps = append(l.belowEps, ep)
	}
	trashed := f.episode(l.carrie, 1, 1, "Carrie E1")
	if err := f.s.MarkItemTrashed(f.ctx, trashed.ID); err != nil {
		t.Fatal(err)
	}
	l.darkEp = f.episode(l.dark, 1, 1, "Secrets")
	f.added(l.darkEp, "2025-12-01 00:00:00")

	l.zodiac = f.item("movie", "Zodiac", 9000)
	f.exec(`UPDATE media_items SET year = 2007 WHERE id = ?`, l.zodiac.ID)
	f.added(l.zodiac, "2026-02-15 00:00:00")
	l.clip = f.item("video", "clip", 60)
	f.added(l.clip, "2026-01-15 00:00:00")
	l.pending = f.item("episode", "Silo S01E01", 3000)
	f.added(l.pending, "2026-01-20 00:00:00")

	var err error
	if l.toWatch, err = f.s.CreateCollection(f.ctx, "to watch"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []error{
		f.s.AddItemToCollection(f.ctx, l.toWatch.ID, l.andorEps[1].ID),
		f.s.AddItemToCollection(f.ctx, l.toWatch.ID, l.zodiac.ID),
		f.s.AddShowToCollection(f.ctx, l.toWatch.ID, l.below.ID),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}
	return l
}

func (l *catalog) list(opts store.ListEntriesOpts) ([]string, int) {
	l.t.Helper()
	entries, total, err := l.s.ListEntries(l.ctx, opts)
	if err != nil {
		l.t.Fatalf("ListEntries(%+v): %v", opts, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		switch e.Kind {
		case store.EntryShow:
			names[i] = "show:" + e.Show.Title
		case store.EntryItem:
			names[i] = e.Item.Title
		default:
			l.t.Fatalf("entry kind %q", e.Kind)
		}
	}
	return names, total
}

func (l *catalog) expect(name string, opts store.ListEntriesOpts, want ...string) {
	l.t.Helper()
	got, total := l.list(opts)
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) || total != len(want) {
		l.t.Errorf("%s: got %v (total %d), want %v", name, got, total, want)
	}
}

func TestListEntriesGridAndFilters(t *testing.T) {
	l := newCatalog(t)

	l.expect("default added desc: shows by newest episode, pending episode as an item", store.ListEntriesOpts{},
		"show:Andor", "Zodiac", "show:Below", "Silo S01E01", "clip", "show:Dark")
	l.expect("added asc", store.ListEntriesOpts{Order: "asc"},
		"show:Dark", "clip", "Silo S01E01", "show:Below", "Zodiac", "show:Andor")
	l.expect("type show", store.ListEntriesOpts{Type: "show"}, "show:Andor", "show:Below", "show:Dark")
	l.expect("type movie", store.ListEntriesOpts{Type: "movie"}, "Zodiac")
	l.expect("title", store.ListEntriesOpts{Sort: "title"},
		"show:Andor", "show:Below", "clip", "show:Dark", "Silo S01E01", "Zodiac")
	l.expect("year asc puts unknown years first", store.ListEntriesOpts{Sort: "year", Type: "show"},
		"show:Dark", "show:Andor", "show:Below")
	l.expect("year desc", store.ListEntriesOpts{Sort: "year", Order: "desc", Type: "movie"}, "Zodiac")

	got, total := l.list(store.ListEntriesOpts{Offset: 1, Limit: 2})
	if !reflect.DeepEqual(got, []string{"Zodiac", "show:Below"}) || total != 6 {
		t.Errorf("page = %v total %d", got, total)
	}
	if got, total := l.list(store.ListEntriesOpts{Offset: 50}); len(got) != 0 || total != 6 {
		t.Errorf("past the end = %v total %d", got, total)
	}

	l.expect("search show title", store.ListEntriesOpts{Query: "and"}, "show:Andor")
	l.expect("episode title hit surfaces its show", store.ListEntriesOpts{Query: "reckon"}, "show:Andor")
	l.expect("search items", store.ListEntriesOpts{Query: "zod"}, "Zodiac")
	l.expect("search pending episode", store.ListEntriesOpts{Query: "silo"}, "Silo S01E01")
	l.expect("search with type", store.ListEntriesOpts{Query: "s", Type: "show", Sort: "title"}, "show:Dark")
	l.expect("punctuation-only query", store.ListEntriesOpts{Query: "!!"})

	l.expect("collection order across items, shows and episode members", store.ListEntriesOpts{CollectionID: l.toWatch.ID},
		"show:Andor", "Zodiac", "show:Below")
	l.expect("collection with explicit sort", store.ListEntriesOpts{CollectionID: l.toWatch.ID, Sort: "title"},
		"show:Andor", "show:Below", "Zodiac")
	l.expect("uncollected", store.ListEntriesOpts{Uncollected: true}, "Silo S01E01", "clip", "show:Dark")

	for _, bad := range []store.ListEntriesOpts{{Type: "episode"}, {Sort: "nope"}, {Order: "up"}} {
		if _, _, err := l.s.ListEntries(l.ctx, bad); !errors.Is(err, store.ErrInvalidInput) {
			t.Errorf("ListEntries(%+v) = %v, want ErrInvalidInput", bad, err)
		}
	}
}

func TestListEntriesProgressAndSummaries(t *testing.T) {
	l := newCatalog(t)
	l.progress(l.andorEps[0], 3000, 3000, true, "2026-07-01 20:00:00")
	l.progress(l.belowEps[0], 100, 3000, false, "2026-09-01 20:00:00")
	l.progress(l.darkEp, 3000, 3000, true, "2026-06-01 20:00:00")
	l.progress(l.zodiac, 100, 9000, false, "2026-08-01 20:00:00")
	l.progress(l.clip, 60, 60, true, "2026-05-01 20:00:00")

	l.expect("watched desc, latest episode progress for shows", store.ListEntriesOpts{Sort: "watched"},
		"show:Below", "Zodiac", "show:Andor", "show:Dark", "clip", "Silo S01E01")
	l.expect("in progress: started shows with an up next, started items", store.ListEntriesOpts{InProgress: true, Sort: "watched"},
		"show:Below", "Zodiac", "show:Andor")

	entries, _, err := l.s.ListEntries(l.ctx, store.ListEntriesOpts{Type: "show", Sort: "title"})
	if err != nil {
		t.Fatal(err)
	}
	andor, below, dark := entries[0].Show, entries[1].Show, entries[2].Show
	if andor.Title != "Andor" || *andor.Year != 2022 || andor.NeedsCheck || andor.SeasonCount != 1 || andor.EpisodeCount != 3 ||
		!andor.Available || !andor.HasProgress || andor.LatestAddedAt != "2026-03-01 00:00:00" {
		t.Errorf("andor summary = %+v", andor)
	}
	if !reflect.DeepEqual(andor.CollectionIDs, []int64{l.toWatch.ID}) || !reflect.DeepEqual(below.CollectionIDs, []int64{l.toWatch.ID}) {
		t.Errorf("collection ids andor %v below %v", andor.CollectionIDs, below.CollectionIDs)
	}
	if up := andor.UpNext; up == nil || up.ItemID != l.andorEps[1].ID || up.Title != "That Would Be Me" ||
		up.Season != 1 || up.Episode != 2 || !up.Available || up.Progress != nil {
		t.Errorf("andor up next = %+v", andor.UpNext)
	}
	if up := below.UpNext; up == nil || up.ItemID != l.belowEps[0].ID || up.Progress == nil || up.Progress.PositionS != 100 {
		t.Errorf("below up next = %+v", below.UpNext)
	}
	if dark.UpNext != nil || !dark.NeedsCheck || len(dark.CollectionIDs) != 0 {
		t.Errorf("dark should be caught up and flagged: %+v", dark)
	}

	// Ruling M10: an offline next episode is still up next, flagged.
	files, _ := l.s.ListFilesForItem(l.ctx, l.andorEps[1].ID)
	if err := l.s.SetFileStatus(l.ctx, files[0].ID, "offline"); err != nil {
		t.Fatal(err)
	}
	summary, err := l.s.GetShowSummary(l.ctx, l.andor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.UpNext == nil || summary.UpNext.ItemID != l.andorEps[1].ID || summary.UpNext.Available || !summary.Available {
		t.Errorf("offline up next = %+v (show available %v)", summary.UpNext, summary.Available)
	}
	if _, err := l.s.GetShowSummary(l.ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing show summary: %v", err)
	}

	states, err := l.s.ShowEpisodeStates(l.ctx, l.andor.ID)
	if err != nil || len(states) != 3 {
		t.Fatalf("episode states = %+v, %v", states, err)
	}
	items, err := l.s.ListShowItems(l.ctx, l.andor.ID)
	if err != nil || len(items) != 3 || items[0].Title != "Kassa" || items[0].Show == nil || items[0].Show.Title != "Andor" || *items[0].Episode != 1 {
		t.Fatalf("show items = %+v, %v", items, err)
	}
}

func TestCollectionsHoldShows(t *testing.T) {
	l := newCatalog(t)
	if err := l.s.AddShowToCollection(l.ctx, l.toWatch.ID, l.below.ID); err != nil {
		t.Fatalf("re-adding a show: %v", err)
	}
	if err := l.s.AddShowToCollection(l.ctx, l.toWatch.ID, 999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing show: %v", err)
	}
	summaries, err := l.s.ListCollections(l.ctx)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("collections = %+v, %v", summaries, err)
	}
	want := []int64{l.andorEps[1].ID, l.zodiac.ID, l.belowEps[0].ID}
	if summaries[0].ItemCount != 3 || !reflect.DeepEqual(summaries[0].ThumbItemIDs, want) {
		t.Fatalf("summary = %+v, want count 3 thumbs %v", summaries[0], want)
	}

	if err := l.s.ReorderCollection(l.ctx, l.toWatch.ID, []store.CollectionRef{{ItemID: l.zodiac.ID}, {ItemID: l.andorEps[1].ID}}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("item-only reorder of a mixed collection: %v", err)
	}
	if err := l.s.ReorderCollection(l.ctx, l.toWatch.ID, []store.CollectionRef{
		{ShowID: l.below.ID}, {ItemID: l.zodiac.ID}, {ItemID: l.andorEps[1].ID},
	}); err != nil {
		t.Fatalf("mixed reorder: %v", err)
	}
	l.expect("reordered", store.ListEntriesOpts{CollectionID: l.toWatch.ID}, "show:Below", "Zodiac", "show:Andor")

	found, _, err := l.s.SearchItemSummaries(l.ctx, store.SearchItemsOpts{Query: "clip", Uncollected: true, Limit: 10})
	if err != nil || len(found) != 1 {
		t.Fatalf("uncollected search with show rows present = %v, %v", found, err)
	}

	if _, err := l.s.RemoveShowFromCollection(l.ctx, l.toWatch.ID, l.below.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.s.RemoveShowFromCollection(l.ctx, l.toWatch.ID, l.below.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
	l.expect("after removing the show", store.ListEntriesOpts{CollectionID: l.toWatch.ID}, "Zodiac", "show:Andor")
	l.invariant()
}

func TestShowMembersCoverTheirEpisodeRows(t *testing.T) {
	l := newCatalog(t)
	if err := l.s.AddItemToCollection(l.ctx, l.toWatch.ID, l.belowEps[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := l.s.ReorderCollection(l.ctx, l.toWatch.ID, []store.CollectionRef{
		{ShowID: l.andor.ID}, {ItemID: l.andorEps[1].ID}, {ShowID: l.below.ID}, {ItemID: l.zodiac.ID},
	}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("show listed with its episode: %v", err)
	}
	if err := l.s.ReorderCollection(l.ctx, l.toWatch.ID, []store.CollectionRef{
		{ShowID: l.andor.ID}, {ItemID: l.zodiac.ID},
	}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("reorder missing a show: %v", err)
	}
	if err := l.s.ReorderCollection(l.ctx, l.toWatch.ID, []store.CollectionRef{
		{ShowID: l.andor.ID}, {ShowID: l.below.ID}, {ItemID: l.zodiac.ID},
	}); err != nil {
		t.Fatalf("show-level reorder: %v", err)
	}
	l.expect("show-level order", store.ListEntriesOpts{CollectionID: l.toWatch.ID}, "show:Andor", "show:Below", "Zodiac")

	removed, err := l.s.RemoveShowFromCollection(l.ctx, l.toWatch.ID, l.below.ID)
	if err != nil || !reflect.DeepEqual(removed, []int64{l.belowEps[0].ID}) {
		t.Fatalf("remove Below = %v, %v", removed, err)
	}
	removed, err = l.s.RemoveShowFromCollection(l.ctx, l.toWatch.ID, l.andor.ID)
	if err != nil || !reflect.DeepEqual(removed, []int64{l.andorEps[1].ID}) {
		t.Fatalf("remove Andor (member through an episode) = %v, %v", removed, err)
	}
	l.expect("after removals", store.ListEntriesOpts{CollectionID: l.toWatch.ID}, "Zodiac")
}
