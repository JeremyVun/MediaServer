package classify

import (
	"testing"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestLaterArrivalNeverMergesAwayALockedItem(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	unmatched := *first.ShowID
	if _, err := h.svc.EditItem(h.ctx, first.ID, ItemEdit{Title: ptr("My title")}); err != nil {
		t.Fatal(err)
	}
	matched, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Special Ops: Lioness", TVmazeID: ptr(int64(50415))})
	if err != nil {
		t.Fatal(err)
	}

	arrival := h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)
	h.invariant()

	if got := h.get(first.ID); got.ShowID == nil || *got.ShowID != unmatched {
		t.Errorf("locked item %d moved to show %d, want it kept in %d", first.ID, derefInt64(got.ShowID), unmatched)
	}
	if *arrival.ShowID != unmatched || len(h.shows()) != 2 {
		t.Errorf("arrival joined %d with %d shows, want the unmatched show %d kept apart from %d", *arrival.ShowID, len(h.shows()), unmatched, matched.ID)
	}
}

func TestOutageRematchUsesEpisodeTitles(t *testing.T) {
	h := newHarness(t)
	h.tv.down.Store(true)
	first := h.classify(h.item("East of Eden - S01E01 - Brothers.mkv", 3060).ID)
	show := *first.ShowID
	h.tv.down.Store(false)
	edenShows(h, false)

	if err := h.svc.RefreshShow(h.ctx, show); err != nil {
		t.Fatal(err)
	}
	if got := h.show(show); derefInt64(got.TVmazeID) != 79399 || got.NeedsCheck {
		t.Errorf("show after the outage = %+v, want matched to 79399 by the episode title", got)
	}
}

func TestOutageRematchUsesEpisodeRuntime(t *testing.T) {
	h := newHarness(t)
	h.tv.down.Store(true)
	first := h.classify(h.item("East.of.Eden.S01E01.mkv", 3060).ID)
	show := *first.ShowID
	h.tv.down.Store(false)
	edenShows(h, true)

	if err := h.svc.RefreshShow(h.ctx, show); err != nil {
		t.Fatal(err)
	}
	if got := h.show(show); derefInt64(got.TVmazeID) != 79399 {
		t.Errorf("show after the outage = %+v, want matched to 79399 by the file's duration", got)
	}
}

func TestOwnerAliasFromAMergeStopsLaterArrivalUpgrades(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	show := *first.ShowID
	stray := h.classify(h.item("Lioness.Extras.S03E09.mkv", 3000).ID)
	if _, err := h.svc.MergeShows(h.ctx, *stray.ShowID, show); err != nil {
		t.Fatal(err)
	}
	if a := h.alias("lioness"); a.Source != store.AliasAuto {
		t.Fatalf("fixture: the arrival's alias = %+v, want auto", a)
	}

	h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)

	if got := h.show(show); got.TVmazeID != nil || !got.NeedsCheck {
		t.Errorf("show = %+v, want it left unmatched: the owner merged into it", got)
	}
}
