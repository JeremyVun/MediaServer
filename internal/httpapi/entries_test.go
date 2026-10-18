package httpapi

import "testing"

func TestLibraryUnwatched(t *testing.T) {
	f := newLibraryFixture(t)
	alien := f.item("movie", "Alien", 1979)
	f.added(alien, "2026-02-10 00:00:00")
	brazil := f.item("movie", "Brazil", 1985)
	f.added(brazil, "2026-02-05 00:00:00")
	f.watched(brazil, 3000, true, "2026-05-04 00:00:00")
	dark := f.show("Dark", 2017)
	darkEp := f.episode(dark, 1, 1, "Secrets")
	f.added(darkEp, "2025-12-01 00:00:00")
	f.watched(darkEp, 0, false, "2026-05-05 00:00:00")
	f.watched(f.clip, 0, false, "2026-05-06 00:00:00")
	for _, step := range []error{
		f.st.AddItemToCollection(f.ctx, f.toWatch.ID, f.andorEps[1].ID),
		f.st.AddItemToCollection(f.ctx, f.toWatch.ID, f.zodiac.ID),
		f.st.AddShowToCollection(f.ctx, f.toWatch.ID, f.below.ID),
		f.st.AddItemToCollection(f.ctx, f.toWatch.ID, alien.ID),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}

	lib := f.expectLibrary("unwatched=1", 5, "Alien", "show:Below", "Silo S01E01", "clip", "show:Dark")
	if below := lib.Entries[1]; below.HasProgress || below.UpNext == nil || below.UpNext.ItemID != f.belowEps[0].ID {
		t.Fatalf("unstarted show = %+v up_next %+v", below, below.UpNext)
	}
	f.expectLibrary("unwatched=1&type=movie", 1, "Alien")
	f.expectLibrary("unwatched=1&type=show", 2, "show:Below", "show:Dark")
	f.expectLibrary("unwatched=1&collection_id="+idStr(f.toWatch.ID), 2, "show:Below", "Alien")
	f.expectLibrary("unwatched=1&uncollected=1", 3, "Silo S01E01", "clip", "show:Dark")
	f.expectLibrary("unwatched=1&sort=title", 5, "Alien", "show:Below", "clip", "show:Dark", "Silo S01E01")
	f.expectLibrary("unwatched=1&limit=2&offset=2", 5, "Silo S01E01", "clip")
	f.expectLibrary("unwatched=1&q=zod", 0)
	f.expectLibrary("unwatched=0", 8, "show:Andor", "Zodiac", "Alien", "Brazil", "show:Below", "Silo S01E01", "clip", "show:Dark")
}
