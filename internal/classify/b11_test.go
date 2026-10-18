package classify

import (
	"fmt"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/store"
)

var edenTitles = []string{"Brothers", "Cathy", "In the Valley", "Kate", "Departure", "The Great Try", "Timshel"}

func titled(season int, titles ...string) []tvEpisode {
	eps := make([]tvEpisode, len(titles))
	for i, title := range titles {
		eps[i] = tvEpisode{season, i + 1, title, "2026-01-01"}
	}
	return eps
}

func numbered(season, n int, format string) []tvEpisode {
	titles := make([]string, n)
	for i := range titles {
		titles[i] = fmt.Sprintf(format, i+1)
	}
	return titled(season, titles...)
}

func repeat(minutes, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = minutes
	}
	return out
}

// edenShows is TVmaze's answer for "east of eden", in its score order
// (ruling B11's production case). runtimes=false drops every runtime.
func edenShows(h *harness, runtimes bool) {
	shows := []tvShow{
		{id: 79399, name: "East of Eden", premiered: "2026-09-25", network: "Netflix", country: "US",
			episodes: titled(1, edenTitles...), runtimes: []int{51, 61, 58, 60, 57, 59, 68}},
		{id: 18548, name: "East of Eden", premiered: "1981-02-08", network: "ABC", country: "US",
			episodes: numbered(1, 3, "Part %d"), runtimes: repeat(120, 3)},
		{id: 15870, name: "East of Eden", premiered: "2008-08-25", network: "MBC", country: "KR",
			episodes: numbered(1, 56, "Episode %d"), runtimes: repeat(65, 56)},
		{id: 87589, name: "East of Eden", premiered: "2025-04-01", network: "Bilibili", country: "CN",
			episodes: numbered(1, 40, "Episode %d"), runtimes: repeat(45, 40)},
	}
	for _, s := range shows {
		if !runtimes {
			s.runtimes = nil
		}
		h.tv.add(s, "east of eden")
	}
}

func TestEpisodeTitlesMatchEastOfEden(t *testing.T) {
	h := newHarness(t)
	h.tv.maxAge = 3600
	edenShows(h, true)
	for e, title := range edenTitles {
		item := h.classify(h.item(fmt.Sprintf("East of Eden - S01E%02d - %s.mkv", e+1, title), 3060).ID)
		if item.Title != title {
			t.Fatalf("E%d title = %q, want %q", e+1, item.Title, title)
		}
	}
	shows := h.shows()
	if len(shows) != 1 || shows[0].TVmazeID == nil || *shows[0].TVmazeID != 79399 || shows[0].NeedsCheck {
		t.Fatalf("shows = %+v, want one East of Eden matched to 79399 and unflagged", shows)
	}
	if n := h.tv.requests("/search/"); n != 1 {
		t.Fatalf("%d searches, want 1", n)
	}
	for id, want := range map[int]int{79399: 1, 18548: 1, 15870: 1, 87589: 0} {
		if n := h.tv.requests(fmt.Sprintf("/shows/%d/episodes", id)); n != want {
			t.Errorf("show %d episode list fetched %d times, want %d: titles reuse the tie-break's lists", id, n, want)
		}
	}
	if n := h.tv.requests("/shows/87589"); n != 0 {
		t.Errorf("%d requests went to the fourth namesake, past the three-candidate cap", n)
	}
}

func TestShowTieBreaks(t *testing.T) {
	cases := []struct {
		name     string
		runtimes bool
		file     string
		durS     float64
		want     int64 // 0: unmatched and flagged
	}{
		{"title alone decides", false, "East of Eden - S01E04 - Kate.mkv", 3600, 79399},
		{"titles normalize like show keys", false, "East of Eden - S01E06 - the great-try.mkv", 3540, 79399},
		{"runtime decides a file with no episode title", true, "East.of.Eden.S01E03.1080p.mkv", 58*60 + 40, 79399},
		{"runtime outside 10 % decides nothing", true, "East.of.Eden.S01E03.1080p.mkv", 75 * 60, 0},
		{"no title and no runtimes stays weak", false, "East.of.Eden.S01E03.1080p.mkv", 3480, 0},
		{"a title no candidate has stays weak", false, "East of Eden - S01E01 - Prologue.mkv", 3060, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			edenShows(h, tc.runtimes)
			item := h.classify(h.item(tc.file, tc.durS).ID)
			show := h.show(*item.ShowID)
			if tc.want == 0 {
				if show.TVmazeID != nil || !show.NeedsCheck {
					t.Fatalf("show = %+v, want unmatched and flagged", show)
				}
				return
			}
			if show.TVmazeID == nil || *show.TVmazeID != tc.want || show.NeedsCheck {
				t.Fatalf("show = %+v, want %d unflagged", show, tc.want)
			}
		})
	}
}

func TestTwoTitleMatches(t *testing.T) {
	sameTitle := func(h *harness, runtimeA, runtimeB int) {
		h.tv.add(tvShow{id: 1, name: "East of Eden", premiered: "2026-01-01", episodes: titled(1, "Brothers"), runtimes: []int{runtimeA}}, "east of eden")
		h.tv.add(tvShow{id: 2, name: "East of Eden", premiered: "1981-01-01", episodes: titled(1, "Brothers"), runtimes: []int{runtimeB}}, "east of eden")
		h.tv.add(tvShow{id: 3, name: "East of Eden", premiered: "2008-01-01", episodes: titled(1, "Episode 1"), runtimes: []int{51}}, "east of eden")
	}
	t.Run("stay weak when runtime cannot split them", func(t *testing.T) {
		h := newHarness(t)
		sameTitle(h, 51, 51)
		item := h.classify(h.item("East of Eden - S01E01 - Brothers.mkv", 3060).ID)
		if show := h.show(*item.ShowID); show.TVmazeID != nil || !show.NeedsCheck {
			t.Fatalf("show = %+v, want unmatched: two candidates share the title", show)
		}
	})
	t.Run("runtime splits them, never reaching the untitled one", func(t *testing.T) {
		h := newHarness(t)
		sameTitle(h, 51, 120)
		item := h.classify(h.item("East of Eden - S01E01 - Brothers.mkv", 3060).ID)
		if show := h.show(*item.ShowID); show.TVmazeID == nil || *show.TVmazeID != 1 {
			t.Fatalf("show = %+v, want 1, the title match whose runtime fits", show)
		}
	})
}

func TestLaterTitledArrivalMatchesEastOfEden(t *testing.T) {
	h := newHarness(t)
	h.tv.maxAge = 3600
	edenShows(h, false)
	first := h.classify(h.item("East of Eden - S01E01.mkv", 3060).ID)
	unmatched := h.show(*first.ShowID)
	if unmatched.TVmazeID != nil || !unmatched.NeedsCheck {
		t.Fatalf("untitled first file: show = %+v, want unmatched", unmatched)
	}
	second := h.classify(h.item("East of Eden - S01E02 - Cathy.mkv", 3660).ID)
	show := h.show(unmatched.ID)
	if *second.ShowID != unmatched.ID || show.TVmazeID == nil || *show.TVmazeID != 79399 || show.NeedsCheck || len(h.shows()) != 1 {
		t.Fatalf("show = %+v, want the flagged show matched to 79399 by the titled arrival (B7.4)", show)
	}
	if got := h.get(first.ID).Title; got != "Brothers" {
		t.Fatalf("first item title = %q, want TVmaze's after the match", got)
	}
}

func TestAKAMatchesNorsemen(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 20974, name: "Vikingane", premiered: "2016-10-14", network: "NRK1", country: "NO",
		episodes: numbered(1, 6, "Episode %d"), akas: []string{"Norsemen", "Les Norsemen"}}, "norsemen")
	h.tv.add(tvShow{id: 9, name: "The Last Kingdom", premiered: "2015-10-10", episodes: seasons(8)}, "norsemen")

	item := h.classify(h.item("Norsemen.S01E01.1080p.mkv", 1800).ID)
	show := h.show(*item.ShowID)
	if show.TVmazeID == nil || *show.TVmazeID != 20974 || show.NeedsCheck || show.Title != "Norsemen" || !show.TitleLocked {
		t.Fatalf("show = %+v, want Vikingane matched under its AKA Norsemen, title locked", show)
	}
	if n := h.tv.requests("/shows/9/akas"); n != 0 {
		t.Fatalf("%d AKA requests for a lower result, want only the top one asked", n)
	}
	if err := h.svc.RefreshShow(h.ctx, show.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.show(show.ID).Title; got != "Norsemen" {
		t.Fatalf("title after refresh = %q, want the AKA kept", got)
	}
}

func TestNoAKAMatchStaysWeak(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 5, name: "Vikings", premiered: "2013-03-03", episodes: seasons(9), akas: []string{"Vikings: Valhalla"}}, "norsemen")
	item := h.classify(h.item("Norsemen.S01E01.1080p.mkv", 1800).ID)
	if show := h.show(*item.ShowID); show.TVmazeID != nil || !show.NeedsCheck || show.Title != "Norsemen" {
		t.Fatalf("show = %+v, want Norsemen unmatched and flagged", show)
	}
	if n := h.tv.requests("/shows/5/akas"); n != 1 {
		t.Fatalf("%d AKA requests, want exactly one", n)
	}
}

func starWarsAndor(h *harness, runtimeMin int) {
	h.tv.add(tvShow{id: 52341, name: "Andor", premiered: "2022-09-21", network: "Disney+", country: "US",
		episodes: titled(1, "Kassa", "That Would Be Me", "Reckoning", "Aldhani", "The Axe Forgets"),
		runtimes: repeat(runtimeMin, 5)}, "star wars andor")
	h.tv.add(tvShow{id: 1259, name: "Star Wars: The Clone Wars", premiered: "2008-10-03", episodes: seasons(22)}, "star wars andor")
}

func TestNearNameJoinsLocalAndor(t *testing.T) {
	h := newHarness(t)
	starWarsAndor(h, 41)
	local, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Andor", TVmazeID: ptr(int64(52341))})
	if err != nil {
		t.Fatal(err)
	}
	item := h.classify(h.item("Star.Wars.Andor.S01E05.The.Axe.Forgets.1080p.WEB.h264.mkv", 2460).ID)
	if item.ShowID == nil || *item.ShowID != local.ID || len(h.shows()) != 1 {
		t.Fatalf("item joined %v (%d shows), want the local Andor %d", item.ShowID, len(h.shows()), local.ID)
	}
	if h.alias("star wars andor").ShowID != local.ID {
		t.Fatal("no alias from star wars andor to Andor")
	}
}

func TestNearNameDahmerByRuntime(t *testing.T) {
	dahmer := func(h *harness) {
		h.tv.add(tvShow{id: 50907, name: "DAHMER - Monster: The Jeffrey Dahmer Story", premiered: "2022-09-21", network: "Netflix",
			country: "US", episodes: numbered(1, 10, "Episode %d"), runtimes: repeat(55, 10)}, "monster the jeffrey dahmer story")
	}
	t.Run("episode present and runtime fits", func(t *testing.T) {
		h := newHarness(t)
		dahmer(h)
		item := h.classify(h.item("Monster.The.Jeffrey.Dahmer.Story.S01E08.1080p.WEB.h264.mkv", 56*60).ID)
		if show := h.show(*item.ShowID); show.TVmazeID == nil || *show.TVmazeID != 50907 || show.NeedsCheck {
			t.Fatalf("show = %+v, want DAHMER 50907 unflagged", show)
		}
	})
	t.Run("runtime off stays weak", func(t *testing.T) {
		h := newHarness(t)
		dahmer(h)
		item := h.classify(h.item("Monster.The.Jeffrey.Dahmer.Story.S01E08.1080p.WEB.h264.mkv", 30*60).ID)
		if show := h.show(*item.ShowID); show.TVmazeID != nil || !show.NeedsCheck {
			t.Fatalf("show = %+v, want unmatched: a near name needs its runtime confirmed", show)
		}
	})
}

func TestNearNameUnconfirmedStaysWeak(t *testing.T) {
	h := newHarness(t)
	starWarsAndor(h, 41)
	item := h.classify(h.item("Star.Wars.Andor.S01E05.Rix.Road.1080p.mkv", 2460).ID)
	if show := h.show(*item.ShowID); show.TVmazeID != nil || !show.NeedsCheck {
		t.Fatalf("show = %+v, want unmatched: a titled file's near name needs its title confirmed", show)
	}
}

func TestMonster2022StaysWeak(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 8244, name: "Monster", premiered: "2004-04-07", episodes: seasons(74)}, "monster")
	h.tv.add(tvShow{id: 50907, name: "DAHMER - Monster: The Jeffrey Dahmer Story", premiered: "2022-09-21",
		episodes: numbered(1, 10, "Episode %d"), runtimes: repeat(55, 10)}, "monster")
	h.tv.add(tvShow{id: 75023, name: "Monsters: The Lyle and Erik Menendez Story", premiered: "2024-09-19",
		episodes: numbered(1, 9, "Episode %d"), runtimes: repeat(55, 9)}, "monster")
	item := h.classify(h.item("Monster.2022.S02E07.1080p.mkv", 55*60).ID)
	if show := h.show(*item.ShowID); show.TVmazeID != nil || !show.NeedsCheck {
		t.Fatalf("show = %+v, want Monster 2022 unmatched (B13 item 3 stays manual)", show)
	}
}
