package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func entryKey(e store.Entry) string {
	if e.Kind == store.EntryItem {
		return fmt.Sprintf("item:%d", e.Item.ID)
	}
	return fmt.Sprintf("show:%d", e.Show.ID)
}

// probe: /api/library pages never duplicate or skip an entry, for every sort, order and type, when shows and items share every sort key.
func TestProbe_Pagination_MixedEqualKeys(t *testing.T) {
	f := newFixture(t)
	coll, err := f.s.CreateCollection(f.ctx, "mixed")
	if err != nil {
		t.Fatal(err)
	}
	const stamp = "2026-01-01 00:00:00"
	for i := 0; i < 24; i++ {
		typ := []string{"movie", "video", "episode"}[i%3]
		item := f.item(typ, "Same", 3000)
		f.added(item, stamp)
		if i%4 == 0 {
			f.progress(item, 10, 3000, false, stamp)
		}
		if i%5 == 0 {
			if err := f.s.AddItemToCollection(f.ctx, coll.ID, item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 12; i++ {
		show := f.show("Same")
		ep := f.episode(show, 1, 1, "Same")
		f.added(ep, stamp)
		if i%3 == 0 {
			f.progress(ep, 10, 3000, false, stamp)
		}
		if i%4 == 0 {
			if err := f.s.AddShowToCollection(f.ctx, coll.ID, show.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.exec(`UPDATE media_items SET year = 2020 WHERE id % 2 = 0`)
	f.exec(`UPDATE shows SET year = 2020 WHERE id % 2 = 0`)

	for _, sortKey := range []string{"", "added", "title", "year", "watched"} {
		for _, order := range []string{"", "asc", "desc"} {
			for _, typ := range []string{"", "show", "movie"} {
				for _, scope := range []string{"all", "collection", "in_progress"} {
					opts := store.ListEntriesOpts{Sort: sortKey, Order: order, Type: typ}
					switch scope {
					case "collection":
						opts.CollectionID = coll.ID
					case "in_progress":
						opts.InProgress = true
					}
					full := opts
					full.Limit = 500
					all, total, err := f.s.ListEntries(f.ctx, full)
					if err != nil {
						t.Fatal(err)
					}
					if len(all) != total {
						t.Fatalf("%+v: %d entries, total %d", full, len(all), total)
					}
					for _, limit := range []int{1, 7} {
						seen := map[string]bool{}
						var paged []string
						for offset := 0; offset < total; offset += limit {
							page := opts
							page.Offset, page.Limit = offset, limit
							entries, pageTotal, err := f.s.ListEntries(f.ctx, page)
							if err != nil {
								t.Fatal(err)
							}
							if pageTotal != total {
								t.Fatalf("%+v: total %d, want %d", page, pageTotal, total)
							}
							for _, e := range entries {
								k := entryKey(e)
								if seen[k] {
									t.Fatalf("%+v: %s appears on two pages", page, k)
								}
								seen[k] = true
								paged = append(paged, k)
							}
						}
						for i, e := range all {
							if i >= len(paged) || paged[i] != entryKey(e) {
								t.Fatalf("%+v limit %d: paged order %v differs from the unpaged %d entries", opts, limit, paged, len(all))
							}
						}
					}
				}
			}
		}
	}
}

// probe: the foreign keys behave as 0008 designs them: show delete cascades aliases, cached episodes and show collection rows, and nulls items' show_id; item purge cascades files, chapters, progress and collection rows; file delete cascades chapters.
func TestProbe_FK_CascadesMatchDesign(t *testing.T) {
	f := newFixture(t)
	show := f.show("Doomed")
	e1, e2 := f.episode(show, 1, 1, "One"), f.episode(show, 1, 2, "Two")
	if _, err := f.s.PutShowAlias(f.ctx, "doomed", show.ID, store.AliasOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReplaceShowEpisodes(f.ctx, show.ID, []store.ShowEpisode{{Season: 1, Number: 3, Title: "Three"}}); err != nil {
		t.Fatal(err)
	}
	coll, err := f.s.CreateCollection(f.ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.AddShowToCollection(f.ctx, coll.ID, show.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AddItemToCollection(f.ctx, coll.ID, e1.ID); err != nil {
		t.Fatal(err)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.s.DB().QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	f.exec(`DELETE FROM shows WHERE id = ?`, show.ID)
	if n := count(`SELECT COUNT(*) FROM show_aliases WHERE show_id = ?`, show.ID); n != 0 {
		t.Errorf("%d aliases outlived their show", n)
	}
	if n := count(`SELECT COUNT(*) FROM show_episodes WHERE show_id = ?`, show.ID); n != 0 {
		t.Errorf("%d cached episodes outlived their show", n)
	}
	if n := count(`SELECT COUNT(*) FROM collection_items WHERE show_id = ?`, show.ID); n != 0 {
		t.Errorf("%d show collection rows outlived their show", n)
	}
	if n := count(`SELECT COUNT(*) FROM collection_items WHERE item_id = ?`, e1.ID); n != 1 {
		t.Errorf("episode collection rows = %d, want the item's row kept", n)
	}
	if n := count(`SELECT COUNT(*) FROM media_items WHERE id IN (?, ?) AND show_id IS NULL`, e1.ID, e2.ID); n != 2 {
		t.Errorf("%d of 2 items had show_id nulled, want ON DELETE SET NULL", n)
	}

	files, err := f.s.ListFilesForItem(f.ctx, e1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReplaceFileChapters(f.ctx, files[0].ID, []store.Chapter{{StartS: 0, EndS: 10, Title: "Intro"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UpsertProgress(f.ctx, e1.ID, 10, 3000, false); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE media_items SET classified_at = NULL WHERE id = ?`, e1.ID)
	if _, err := f.s.HardDeleteItem(f.ctx, e1.ID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM media_files WHERE item_id = ?`,
		`SELECT COUNT(*) FROM watch_progress WHERE item_id = ?`,
		`SELECT COUNT(*) FROM collection_items WHERE item_id = ?`,
	} {
		if n := count(q, e1.ID); n != 0 {
			t.Errorf("%s = %d after the item's purge", q, n)
		}
	}
	if n := count(`SELECT COUNT(*) FROM media_chapters WHERE file_id = ?`, files[0].ID); n != 0 {
		t.Errorf("%d chapters outlived their purged item's file", n)
	}

	files2, err := f.s.ListFilesForItem(f.ctx, e2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReplaceFileChapters(f.ctx, files2[0].ID, []store.Chapter{{StartS: 0, EndS: 10, Title: "Intro"}}); err != nil {
		t.Fatal(err)
	}
	f.exec(`DELETE FROM media_files WHERE id = ?`, files2[0].ID)
	if n := count(`SELECT COUNT(*) FROM media_chapters WHERE file_id = ?`, files2[0].ID); n != 0 {
		t.Errorf("%d chapters outlived their file", n)
	}
}

// probe: PRAGMA foreign_keys is on for every pooled connection, not just the first.
func TestProbe_FK_EveryPooledConnectionEnforces(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var conns []*sql.Conn
	for i := 0; i < 6; i++ {
		c, err := s.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var on int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil {
			t.Fatal(err)
		}
		if on != 1 {
			t.Errorf("connection %d has foreign_keys = %d", i, on)
		}
	}
	for _, c := range conns {
		c.Close()
	}
}

// probe: a show on an unmounted root is a state: it keeps its episodes in the grid, reads unavailable, and up next is the true next episode, offline (M10).
func TestProbe_Offline_ShowKeepsEpisodesAndUpNext(t *testing.T) {
	f := newFixture(t)
	show := f.show("Away")
	e1, e2, e3 := f.episode(show, 1, 1, "One"), f.episode(show, 1, 2, "Two"), f.episode(show, 1, 3, "Three")
	f.progress(e1, 3000, 3000, true, "2026-01-01 10:00:00")
	if err := f.s.SetRootOnline(f.ctx, f.root.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetRootFilesStatus(f.ctx, f.root.ID, "offline"); err != nil {
		t.Fatal(err)
	}

	for _, opts := range []store.ListEntriesOpts{{}, {Type: "show"}, {InProgress: true}} {
		entries, _, err := f.s.ListEntries(f.ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		var sum *store.ShowSummary
		for _, e := range entries {
			if e.Kind == store.EntryShow && e.Show.ID == show.ID {
				sum = e.Show
			}
		}
		if sum == nil {
			t.Fatalf("%+v: offline show missing from the grid", opts)
		}
		if sum.EpisodeCount != 3 || sum.Available || sum.UpNext == nil || sum.UpNext.ItemID != e2.ID || sum.UpNext.Available {
			t.Errorf("%+v: summary = %+v up_next = %+v, want 3 episodes, unavailable, up next E2 offline", opts, sum, sum.UpNext)
		}
	}
	states, err := f.s.ShowEpisodeStates(f.ctx, show.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 {
		t.Errorf("states = %d, want all 3 episodes while offline", len(states))
	}
	for _, id := range []int64{e1.ID, e2.ID, e3.ID} {
		item, err := f.s.GetItem(f.ctx, id)
		if err != nil || item.DeletedAt != nil || item.ShowID == nil {
			t.Errorf("item %d = %+v (%v), want it untouched by the unmount", id, item, err)
		}
	}
}

// probe: completion counts position ≥ credits − 10 s exactly at the boundary, and uses the first online file's credits (M5, B7.8).
func TestProbe_Progress_CreditsToleranceAndFirstOnlineFile(t *testing.T) {
	f := newFixture(t)
	item := f.item("episode", "Ep", 3000)
	files, err := f.s.ListFilesForItem(f.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReplaceFileChapters(f.ctx, files[0].ID, []store.Chapter{{StartS: 2700, EndS: 3000, Title: "Credits"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		position float64
		explicit bool
		want     bool
	}{
		{2690, false, true},
		{2689.9, false, false},
		{10, true, true},
		{100, false, false},
		{2850, false, true},
	} {
		got, err := f.s.UpsertProgress(f.ctx, item.ID, tc.position, 3000, tc.explicit)
		if err != nil {
			t.Fatal(err)
		}
		if got.Completed != tc.want {
			t.Errorf("position %v explicit %v: completed = %v, want %v", tc.position, tc.explicit, got.Completed, tc.want)
		}
	}

	second, err := f.s.CreateFile(f.ctx, store.NewFile{ItemID: item.ID, RootID: f.root.ID, RelPath: "second/Ep.mkv", Size: 1, Mtime: time.Now(), Fingerprint: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.UpdateFileProbe(f.ctx, second.ID, store.ProbeResult{Container: "mkv", DurationS: 3000}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReplaceFileChapters(f.ctx, second.ID, []store.Chapter{{StartS: 2300, EndS: 3000, Title: "Credits"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.UpsertProgress(f.ctx, item.ID, 2400, 3000, false); got.Completed {
		t.Errorf("position 2400 completed by the second file's credits while the first file is online")
	}
	if err := f.s.SetFileStatus(f.ctx, files[0].ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.UpsertProgress(f.ctx, item.ID, 2400, 3000, false); !got.Completed {
		t.Errorf("position 2400 not completed by the first online file's (the second's) credits")
	}
}

// probe: a mixed collection reorders by grid entries, with shows covering their episode rows and hidden rows (trashed item, emptied show) keeping their place (B3, 8c).
func TestProbe_Collections_ReorderMixedEntries(t *testing.T) {
	f := newFixture(t)
	coll, err := f.s.CreateCollection(f.ctx, "mixed")
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := f.show("A"), f.show("B"), f.show("C")
	a1, a2 := f.episode(a, 1, 1, "A1"), f.episode(a, 1, 2, "A2")
	b1 := f.episode(b, 1, 1, "B1")
	c1 := f.episode(c, 1, 1, "C1")
	movie := f.item("movie", "Movie", 7200)
	trashed := f.item("movie", "Trashed", 7200)
	for _, add := range []func() error{
		func() error { return f.s.AddShowToCollection(f.ctx, coll.ID, a.ID) },
		func() error { return f.s.AddItemToCollection(f.ctx, coll.ID, trashed.ID) },
		func() error { return f.s.AddItemToCollection(f.ctx, coll.ID, a2.ID) },
		func() error { return f.s.AddItemToCollection(f.ctx, coll.ID, movie.ID) },
		func() error { return f.s.AddShowToCollection(f.ctx, coll.ID, c.ID) },
		func() error { return f.s.AddItemToCollection(f.ctx, coll.ID, b1.ID) },
	} {
		if err := add(); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.MarkItemsTrashed(f.ctx, []int64{trashed.ID, c1.ID}); err != nil {
		t.Fatal(err)
	}
	_ = a1

	if err := f.s.ReorderCollection(f.ctx, coll.ID, []store.CollectionRef{{ShowID: b.ID}, {ItemID: movie.ID}, {ShowID: a.ID}}); err != nil {
		t.Fatalf("reorder by grid entries: %v", err)
	}
	entries, _, err := f.s.ListEntries(f.ctx, store.ListEntriesOpts{CollectionID: coll.ID})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, entryKey(e))
	}
	want := []string{fmt.Sprintf("show:%d", b.ID), fmt.Sprintf("item:%d", movie.ID), fmt.Sprintf("show:%d", a.ID)}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("grid order = %v, want %v", got, want)
	}

	if err := f.s.MarkItemRestored(f.ctx, trashed.ID); err != nil {
		t.Fatal(err)
	}
	entries, _, err = f.s.ListEntries(f.ctx, store.ListEntriesOpts{CollectionID: coll.ID})
	if err != nil {
		t.Fatal(err)
	}
	got = got[:0]
	for _, e := range entries {
		got = append(got, entryKey(e))
	}
	t.Logf("after restoring the trashed movie the grid reads %v", got)
	if len(got) != 4 {
		t.Errorf("restored item missing: %v", got)
	}

	for name, members := range map[string][]store.CollectionRef{
		"a show omitted":                 {{ShowID: b.ID}, {ItemID: movie.ID}},
		"a show with its episode":        {{ShowID: b.ID}, {ItemID: movie.ID}, {ShowID: a.ID}, {ItemID: a2.ID}},
		"an episode instead of its show": {{ShowID: b.ID}, {ItemID: movie.ID}, {ItemID: a2.ID}},
	} {
		if err := f.s.ReorderCollection(f.ctx, coll.ID, append(members, store.CollectionRef{ItemID: trashed.ID})); err == nil {
			t.Errorf("%s: reorder accepted", name)
		}
	}
}

// probe (RULING NEEDED, characterization): item_count counts collection rows (each episode row counts), while the grid shows one card per show.
func TestProbe_RulingNeeded_CollectionItemCountVsGridCards(t *testing.T) {
	f := newFixture(t)
	coll, err := f.s.CreateCollection(f.ctx, "euphoria")
	if err != nil {
		t.Fatal(err)
	}
	show := f.show("Euphoria")
	for e := 1; e <= 3; e++ {
		ep := f.episode(show, 1, e, fmt.Sprint(e))
		if err := f.s.AddItemToCollection(f.ctx, coll.ID, ep.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.AddShowToCollection(f.ctx, coll.ID, show.ID); err != nil {
		t.Fatal(err)
	}
	colls, err := f.s.ListCollections(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, total, err := f.s.ListEntries(f.ctx, store.ListEntriesOpts{CollectionID: coll.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("a collection holding a show and 3 of its episodes: item_count = %d, grid cards = %d", colls[0].ItemCount, total)
}

// probe: merging a show into one that shares its collection leaves exactly one row there, and episode rows keep membership (B3).
func TestProbe_Collections_MergeKeepsOneRow(t *testing.T) {
	f := newFixture(t)
	coll, err := f.s.CreateCollection(f.ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	from, into := f.show("From"), f.show("Into")
	f.episode(from, 1, 1, "F1")
	f.episode(into, 1, 1, "I1")
	for _, id := range []int64{into.ID, from.ID} {
		if err := f.s.AddShowToCollection(f.ctx, coll.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.MergeShow(f.ctx, from.ID, into.ID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.s.DB().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM collection_items WHERE collection_id = ?`, coll.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("collection rows = %d, want 1", rows)
	}
	entries, total, err := f.s.ListEntries(f.ctx, store.ListEntriesOpts{CollectionID: coll.ID})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || entries[0].Show == nil || entries[0].Show.ID != into.ID || entries[0].Show.EpisodeCount != 2 {
		t.Errorf("entries = %v, want the merged show with 2 episodes", entries)
	}
}
