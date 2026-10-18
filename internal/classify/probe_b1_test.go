package classify

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

// visible fails when a live item can't be reached from the library grid:
// an item outside any show is an item entry, an episode's show is a show
// entry (build ruling B1: nothing disappears from /api/library).
func (h *harness) visible(step string, ids []int64) {
	h.t.Helper()
	entries, _, err := h.st.ListEntries(h.ctx, store.ListEntriesOpts{Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	items, shows := map[int64]bool{}, map[int64]bool{}
	for _, e := range entries {
		if e.Kind == store.EntryItem {
			items[e.Item.ID] = true
			if e.Item.Show != nil {
				h.t.Errorf("%s: item entry %d carries show %d", step, e.Item.ID, e.Item.Show.ID)
			}
		} else {
			shows[e.Show.ID] = true
		}
	}
	for _, id := range ids {
		item, err := h.st.GetItem(h.ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && item.DeletedAt != nil) {
			continue
		}
		if err != nil {
			h.t.Fatal(err)
		}
		switch {
		case item.ShowID == nil && !items[id]:
			h.t.Errorf("%s: live item %d (%s, %q) is missing from the grid", step, id, item.Type, item.Title)
		case item.ShowID != nil && !shows[*item.ShowID]:
			h.t.Errorf("%s: live episode %d's show %d is missing from the grid", step, id, *item.ShowID)
		}
	}
	h.invariant()
}

// probe: every write path keeps type='episode' ⇔ show_id NOT NULL for classified items, and no live item ever leaves /api/library (B1).
func TestProbe_Invariant_EveryWritePathKeepsB1AndVisibility(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	lionessShows(h)
	var ids []int64
	add := func(path string, d float64) int64 {
		id := h.item(path, d).ID
		ids = append(ids, id)
		return id
	}
	a1, a2, a3 := add("Andor.S01E01.mkv", 2400), add("Andor.S01E02.mkv", 2400), add("Andor.S01E03.mkv", 2400)
	l1 := add("Lioness.S03E01.mkv", 3000)
	movie := add("Long.Film.2010.mkv", 7200)
	video := add("clip.mkv", 60)
	pending := add("S01E04.mkv", 1500)
	h.visible("created", ids)
	for _, id := range ids {
		h.classify(id)
	}
	h.visible("classified", ids)
	showA, showL := *h.get(a1).ShowID, *h.get(l1).ShowID
	if h.get(pending).ClassifiedAt != nil {
		t.Fatalf("fixture: %d should stay pending without a show name", pending)
	}

	edit := func(step string, id int64, e ItemEdit, wantErr error) {
		t.Helper()
		_, err := h.svc.EditItem(h.ctx, id, e)
		if (wantErr == nil && err != nil) || (wantErr != nil && !errors.Is(err, wantErr)) {
			t.Fatalf("%s: err = %v, want %v", step, err, wantErr)
		}
		h.visible(step, ids)
	}
	edit("move to the unmatched show", a3, ItemEdit{ShowID: &showL}, nil)
	edit("detach as a movie", a2, ItemEdit{Type: ptr("movie")}, nil)
	edit("back to an episode", a2, ItemEdit{ShowID: &showA}, nil)
	edit("episode without a show", video, ItemEdit{Type: ptr("episode")}, ErrEpisodeNeedsShow)
	edit("pending episode filed by a move", pending, ItemEdit{ShowID: &showL}, nil)
	edit("pending-born video retyped episode in place", movie, ItemEdit{Type: ptr("episode"), ShowID: &showA, Season: ptr(9), Episode: ptr(1)}, nil)
	edit("season edit", a1, ItemEdit{Season: ptr(0)}, nil)
	edit("detach a show's first episode", l1, ItemEdit{Type: ptr("video")}, nil)

	if _, err := h.svc.MergeShows(h.ctx, showL, showA); err != nil {
		t.Fatal(err)
	}
	h.visible("merge", ids)
	if _, err := h.svc.ChangeMatch(h.ctx, showA, Match{}); err != nil {
		t.Fatal(err)
	}
	h.visible("not on TVmaze", ids)
	if _, err := h.svc.ChangeMatch(h.ctx, showA, Match{TVmazeID: ptr(int64(1)), SeasonOffset: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	h.visible("rematched with an offset", ids)
	if err := h.svc.RefreshShow(h.ctx, showA); err != nil {
		t.Fatal(err)
	}
	h.visible("refresh", ids)

	if err := h.st.MarkItemsTrashed(h.ctx, []int64{a1}); err != nil {
		t.Fatal(err)
	}
	h.visible("trash one", ids)
	if err := h.st.MarkItemRestored(h.ctx, a1); err != nil {
		t.Fatal(err)
	}
	h.visible("restore one", ids)

	members, err := h.st.ListItemsByShow(h.ctx, showA)
	if err != nil {
		t.Fatal(err)
	}
	var memberIDs []int64
	for _, m := range members {
		memberIDs = append(memberIDs, m.ID)
	}
	if err := h.st.MarkItemsTrashed(h.ctx, memberIDs); err != nil {
		t.Fatal(err)
	}
	h.visible("trash the show", ids)
	if err := h.svc.HardDeleteItem(h.ctx, memberIDs[0]); err != nil {
		t.Fatal(err)
	}
	h.visible("purge one trashed episode", ids)
	for _, id := range memberIDs[1:] {
		if err := h.st.MarkItemRestored(h.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	h.visible("restore the show", ids)
	if _, err := h.st.GetShow(h.ctx, showA); err != nil {
		t.Fatalf("show %d gone after a partial purge: %v", showA, err)
	}

	for _, id := range memberIDs[1:] {
		edit(fmt.Sprintf("detach %d", id), id, ItemEdit{Type: ptr("video")}, nil)
	}
	if _, err := h.st.GetShow(h.ctx, showA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("show %d outlived its last item: err = %v", showA, err)
	}
	again := add("Andor.S01E05.mkv", 2400)
	h.classify(again)
	h.visible("arrival after the show emptied", ids)
}

type snapshot struct {
	aliases map[string]store.ShowAlias
	items   map[int64]store.Item
	shows   map[int64]store.Show
}

func (h *harness) snapshot(itemIDs ...int64) snapshot {
	h.t.Helper()
	s := snapshot{aliases: map[string]store.ShowAlias{}, items: map[int64]store.Item{}, shows: map[int64]store.Show{}}
	for _, show := range h.shows() {
		s.shows[show.ID] = show
		aliases, err := h.st.ListShowAliases(h.ctx, show.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, a := range aliases {
			s.aliases[a.Key] = a
		}
	}
	for _, id := range itemIDs {
		s.items[id] = h.get(id)
	}
	return s
}

// aliasDiff lists the alias rows that differ between two snapshots.
func aliasDiff(before, after snapshot) []string {
	keys := slices.Sorted(maps.Keys(before.aliases))
	for k := range after.aliases {
		if _, ok := before.aliases[k]; !ok {
			keys = append(keys, k)
		}
	}
	var out []string
	for _, k := range keys {
		if before.aliases[k] != after.aliases[k] {
			out = append(out, fmt.Sprintf("%q: %+v → %+v", k, before.aliases[k], after.aliases[k]))
		}
	}
	return out
}

// probe: every row of "Learning from edits" writes exactly the alias and lock it says, and nothing else.
func TestProbe_Learning_TableRowsWriteExactly(t *testing.T) {
	setup := func(t *testing.T) (*harness, int64, int64, int64, int64) {
		h := newHarness(t)
		h.tv.add(andorShow(), "andor")
		lionessShows(h)
		a := h.classify(h.item("Andor.S01E01.mkv", 2400).ID)
		h.classify(h.item("Andor.S01E02.mkv", 2400).ID) // keeps the show alive when a moves out
		l := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
		if _, err := h.st.PutShowAlias(h.ctx, "star wars andor", *a.ShowID, store.AliasAuto); err != nil {
			t.Fatal(err)
		}
		return h, a.ID, l.ID, *a.ShowID, *l.ShowID
	}

	t.Run("looks right on an item", func(t *testing.T) {
		h, _, l, _, _ := setup(t)
		before := h.snapshot(l)
		if _, err := h.svc.ConfirmItem(h.ctx, l); err != nil {
			t.Fatal(err)
		}
		after := h.snapshot(l)
		if d := aliasDiff(before, after); len(d) > 0 {
			t.Errorf("aliases changed: %v", d)
		}
		if got := after.items[l]; got.NeedsCheck || got.Locked != before.items[l].Locked || got.Title != before.items[l].Title {
			t.Errorf("item = %+v, want only needs_check cleared", got)
		}
	})
	t.Run("looks right on a show", func(t *testing.T) {
		h, _, _, _, showL := setup(t)
		before := h.snapshot()
		if _, err := h.svc.ConfirmShow(h.ctx, showL); err != nil {
			t.Fatal(err)
		}
		after := h.snapshot()
		if d := aliasDiff(before, after); len(d) > 0 {
			t.Errorf("aliases changed: %v", d)
		}
		if got := after.shows[showL]; got.NeedsCheck || got.TitleLocked || got.TVmazeID != nil {
			t.Errorf("show = %+v, want only needs_check cleared", got)
		}
	})
	t.Run("rename", func(t *testing.T) {
		h, _, _, _, showL := setup(t)
		before := h.snapshot()
		if _, err := h.svc.RenameShow(h.ctx, showL, "Lioness (mine)"); err != nil {
			t.Fatal(err)
		}
		after := h.snapshot()
		if d := aliasDiff(before, after); len(d) > 0 {
			t.Errorf("aliases changed: %v", d)
		}
		if got := after.shows[showL]; !got.TitleLocked || got.NeedsCheck != before.shows[showL].NeedsCheck {
			t.Errorf("show = %+v, want title_locked and needs_check untouched", got)
		}
	})
	t.Run("change match", func(t *testing.T) {
		h, _, l, _, showL := setup(t)
		before := h.snapshot(l)
		if _, err := h.svc.ChangeMatch(h.ctx, showL, Match{TVmazeID: ptr(int64(77))}); err != nil {
			t.Fatal(err)
		}
		after := h.snapshot(l)
		if d := aliasDiff(before, after); len(d) > 0 {
			t.Errorf("aliases changed: %v", d)
		}
		if after.shows[showL].NeedsCheck || after.items[l].Locked {
			t.Errorf("show %+v item %+v, want needs_check cleared and the item unlocked", after.shows[showL], after.items[l])
		}
	})
	t.Run("edit title", func(t *testing.T) {
		h, a, _, _, _ := setup(t)
		before := h.snapshot(a)
		if _, err := h.svc.EditItem(h.ctx, a, ItemEdit{Title: ptr("Mine")}); err != nil {
			t.Fatal(err)
		}
		after := h.snapshot(a)
		if d := aliasDiff(before, after); len(d) > 0 {
			t.Errorf("aliases changed: %v", d)
		}
		if got := after.items[a]; !got.Locked || got.NeedsCheck {
			t.Errorf("item = %+v, want locked with needs_check cleared", got)
		}
	})
	t.Run("move", func(t *testing.T) {
		h, a, _, showA, showL := setup(t)
		before := h.snapshot(a)
		if _, err := h.svc.MoveEpisode(h.ctx, a, showL); err != nil {
			t.Fatal(err)
		}
		after := h.snapshot(a)
		want := []string{fmt.Sprintf("%q: %+v → %+v", "andor",
			store.ShowAlias{Key: "andor", ShowID: showA, Source: store.AliasAuto},
			store.ShowAlias{Key: "andor", ShowID: showL, Source: store.AliasOwner})}
		if d := aliasDiff(before, after); !slices.Equal(d, want) {
			t.Errorf("alias diff = %v, want %v", d, want)
		}
		if !after.items[a].Locked {
			t.Errorf("item not locked")
		}
	})
	t.Run("merge", func(t *testing.T) {
		h, a, l, showA, showL := setup(t)
		before := h.snapshot(a, l)
		if _, err := h.svc.MergeShows(h.ctx, showA, showL); err != nil {
			t.Fatal(err)
		}
		after := h.snapshot(a, l)
		for k, was := range before.aliases {
			got := after.aliases[k]
			want := was
			if was.ShowID == showA {
				want.ShowID, want.Source = showL, store.AliasOwner
			}
			if got != want {
				t.Errorf("alias %q = %+v, want %+v (repointed as owner aliases, ruling B17.3)", k, got, want)
			}
		}
		for id, was := range before.items {
			if after.items[id].Locked != was.Locked {
				t.Errorf("item %d locked %v → %v by a merge", id, was.Locked, after.items[id].Locked)
			}
		}
	})
}

// probe (ruling B17.2): an item PATCH that re-sends the item's current show_id is not a move: no lock, no alias, so B7.4 still runs.
func TestProbe_Ruled_UnchangedShowIDIsNotAMove(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	show := *first.ShowID
	if _, err := h.svc.EditItem(h.ctx, first.ID, ItemEdit{ShowID: &show}); err != nil {
		t.Fatal(err)
	}
	got, alias := h.get(first.ID), h.alias("lioness")
	if got.Locked || got.NeedsCheck != first.NeedsCheck || alias.Source != store.AliasAuto {
		t.Errorf("re-sending show_id %d: item locked=%v needs_check=%v (was %v); alias source=%s, want unchanged",
			show, got.Locked, got.NeedsCheck, first.NeedsCheck, alias.Source)
	}
	later := h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)
	if *later.ShowID != show || derefInt64(h.show(show).TVmazeID) != 50415 {
		t.Errorf("later confident arrival: show %d tvmaze=%v, want show %d matched to 50415", *later.ShowID, h.show(show).TVmazeID, show)
	}
}

// probe (ruling B17.5): an owner's year edit locks the item, so a pending item's classify keeps it (B10.1 never wipes it).
func TestProbe_Ruled_OwnerYearEditBeforeClassify(t *testing.T) {
	h := newHarness(t)
	item := h.item("Blade.Runner.mkv", 7000)
	if _, err := h.svc.EditItem(h.ctx, item.ID, ItemEdit{Year: ptr(1982)}); err != nil {
		t.Fatal(err)
	}
	h.svc.Classify(h.ctx, item.ID)
	if got := h.get(item.ID); got.Year == nil || *got.Year != 1982 || !got.Locked {
		t.Errorf("owner set year 1982 before classify; after classify year=%v locked=%v, want 1982 and locked", got.Year, got.Locked)
	}
}

// probe (ruling B17.4): a key with a country code whose only name match is from another country is weak.
func TestProbe_Ruled_SingleNameMatchFromAnotherCountry(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 9, name: "Euphoria", premiered: "2012-01-01", network: "HOT3", country: "IL"}, "euphoria us", "euphoria")
	got := h.classify(h.item("Euphoria.US.S01E01.mkv", 3300).ID)
	if show := h.show(*got.ShowID); show.TVmazeID != nil || !show.NeedsCheck {
		t.Errorf("Euphoria.US with only the Israeli Euphoria on TVmaze: show tvmaze=%d needs_check=%v, want unmatched and flagged",
			derefInt64(show.TVmazeID), show.NeedsCheck)
	}
}

// probe (ruling B17.4): a sole name match with no country still falls back to B4.
func TestProbe_Ruled_SingleNameMatchWithNoCountry(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 9, name: "Euphoria", premiered: "2019-01-01"}, "euphoria us", "euphoria")
	got := h.classify(h.item("Euphoria.US.S01E01.mkv", 3300).ID)
	if show := h.show(*got.ShowID); derefInt64(show.TVmazeID) != 9 {
		t.Errorf("Euphoria.US with one country-less Euphoria: show tvmaze=%d, want 9", derefInt64(show.TVmazeID))
	}
}

// probe (ruling B17.3): an owner merge into a flagged unmatched show marks the merged aliases owner, so no later arrival re-matches the owner's merge target.
func TestProbe_Ruled_OwnerMergeTargetNotRematched(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	target := mustShow(t, h, "Lioness (my pick)")
	target, err := h.st.UpdateShow(h.ctx, target.ID, store.UpdateShowParams{NeedsCheck: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.MergeShows(h.ctx, *first.ShowID, target.ID); err != nil {
		t.Fatal(err)
	}
	h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)
	got := h.show(target.ID)
	if got.Title != target.Title || got.TVmazeID != nil || !got.NeedsCheck || h.alias("lioness").Source != store.AliasOwner {
		t.Errorf("after the owner merged into %q, a later arrival left it: title=%q tvmaze=%d needs_check=%v alias source=%s",
			target.Title, got.Title, derefInt64(got.TVmazeID), got.NeedsCheck, h.alias("lioness").Source)
	}
}

// probe (ruling B17.5): the backfill lock heuristic compares the year too, so a legacy item whose year the owner set keeps it.
func TestProbe_Ruled_LegacyOwnerYearKept(t *testing.T) {
	h := newHarness(t, withClock(time.Hour))
	if err := h.svc.MarkLegacyCutoff(h.ctx); err != nil {
		t.Fatal(err)
	}
	item := h.itemAs("Blade.Runner.mkv", 7000, store.NewItem{Type: "video", Title: "Blade Runner", Year: ptr(1982)})
	got := h.classify(item.ID)
	if got.Year == nil || *got.Year != 1982 || !got.Locked {
		t.Errorf("legacy item with an owner-set year 1982 (v1 parsed none): after classify year=%v locked=%v type=%s, want 1982 and locked",
			got.Year, got.Locked, got.Type)
	}
}
