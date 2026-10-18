package classify

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/semantic"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func andorShow() tvShow {
	show := tvShow{id: 1, name: "Andor", premiered: "2022-09-21", summary: "<p>The <b>rebel</b> years.</p>", poster: true, backdrop: true}
	for n := 1; n <= 10; n++ {
		show.episodes = append(show.episodes, tvEpisode{1, n, fmt.Sprintf("Chapter %d", n), "2022-09-21"})
	}
	show.episodes = append(show.episodes, tvEpisode{2, 1, "One Year Later", "2025-04-22"})
	return show
}

func TestGuess(t *testing.T) {
	cases := []struct {
		relPath   string
		durationS float64
		want      store.NewItem
	}{
		{"Andor.S01E02.1080p.mkv", 2400, store.NewItem{Type: "episode", Title: "Episode 2", ShowKey: ptr("andor"), Season: ptr(1), Episode: ptr(2)}},
		{"Show.S01E01-E02.mkv", 2400, store.NewItem{Type: "episode", Title: "Episodes 1–2", ShowKey: ptr("show"), Season: ptr(1), Episode: ptr(1), EpisodeEnd: ptr(2)}},
		{"Long.Film.2010.mkv", 3600, store.NewItem{Type: "movie", Title: "Long Film", Year: ptr(2010)}},
		{"Long.Film.2010.mkv", 3599, store.NewItem{Type: "video", Title: "Long Film", Year: ptr(2010)}},
		{"Episode.Length.S01E01.mkv", 7200, store.NewItem{Type: "episode", Title: "Episode 1", ShowKey: ptr("episode length"), Season: ptr(1), Episode: ptr(1)}},
		{"[abc].mkv", 60, store.NewItem{Type: "video", Title: "[abc]"}},
	}
	for _, tc := range cases {
		got := Guess(tc.relPath, tc.durationS)
		if fmt.Sprint(deref(got)) != fmt.Sprint(deref(tc.want)) {
			t.Errorf("Guess(%q, %v) = %v, want %v", tc.relPath, tc.durationS, deref(got), deref(tc.want))
		}
	}
}

func deref(n store.NewItem) []any {
	v := func(p *int) any {
		if p == nil {
			return nil
		}
		return *p
	}
	key := any(nil)
	if n.ShowKey != nil {
		key = *n.ShowKey
	}
	return []any{n.Type, n.Title, v(n.Year), key, v(n.Season), v(n.Episode), v(n.EpisodeEnd)}
}

func TestSeasonPackRaceFormsOneShow(t *testing.T) {
	for _, matched := range []bool{true, false} {
		t.Run(fmt.Sprintf("matched=%v", matched), func(t *testing.T) {
			h := newHarness(t)
			h.tv.delay = 5 * time.Millisecond
			if matched {
				h.tv.add(andorShow(), "andor")
			}
			var ids []int64
			for n := 1; n <= 10; n++ {
				ids = append(ids, h.item(fmt.Sprintf("Andor S01 1080p/Andor.S01E%02d.1080p.mkv", n), 2400).ID)
			}
			var wg sync.WaitGroup
			errs := make(chan error, len(ids))
			for _, id := range ids {
				wg.Add(1)
				go func(id int64) {
					defer wg.Done()
					errs <- h.svc.Classify(h.ctx, id)
				}(id)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("classify: %v", err)
				}
			}
			h.invariant()
			shows := h.shows()
			if len(shows) != 1 {
				t.Fatalf("season pack formed %d shows, want 1: %+v", len(shows), shows)
			}
			for _, id := range ids {
				if item := h.get(id); item.ShowID == nil || *item.ShowID != shows[0].ID || item.ClassifiedAt == nil {
					t.Fatalf("item %d = %+v, want filed under show %d", id, item, shows[0].ID)
				}
			}
			if a := h.alias("andor"); a.ShowID != shows[0].ID || a.Source != store.AliasAuto {
				t.Fatalf("alias = %+v", a)
			}
		})
	}
}

func TestAliasHitSkipsTVmaze(t *testing.T) {
	h := newHarness(t)
	show, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "The Bear"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutShowAlias(h.ctx, "the bear", show.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	item := h.classify(h.item("The.Bear.S02E03.mkv", 1800).ID)
	if item.ShowID == nil || *item.ShowID != show.ID || item.Title != "Episode 3" || *item.Season != 2 {
		t.Fatalf("item = %+v", item)
	}
	if hits := h.tv.hits.Load(); hits != 0 {
		t.Fatalf("alias hit still made %d TVmaze requests", hits)
	}
	evs := h.drain()
	if !showEvent(evs, events.ShowUpdated, show.ID) || !itemEvent(evs, item.ID) {
		t.Fatalf("events = %+v", evs)
	}
}

func TestOwnerAliasBeatsTVmaze(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 2, name: "Euphoria", premiered: "2019-06-16"}, "euphoria")
	mine, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "My Euphoria"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutShowAlias(h.ctx, "euphoria", mine.ID, store.AliasOwner); err != nil {
		t.Fatal(err)
	}
	item := h.classify(h.item("Euphoria.S01E01.mkv", 3300).ID)
	if item.ShowID == nil || *item.ShowID != mine.ID {
		t.Fatalf("item joined show %v, want the owner's alias show %d", item.ShowID, mine.ID)
	}
	if len(h.shows()) != 1 || h.tv.hits.Load() != 0 {
		t.Fatalf("shows = %+v, TVmaze hits = %d", h.shows(), h.tv.hits.Load())
	}
	if a := h.alias("euphoria"); a.ShowID != mine.ID || a.Source != store.AliasOwner {
		t.Fatalf("owner alias overwritten: %+v", a)
	}
}

func TestCountryCodeAndYearRetries(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 2, name: "Euphoria", premiered: "2019-06-16"}, "euphoria")
	h.tv.add(tvShow{id: 3, name: "Lanterns", premiered: "2025-08-17"}, "lanterns")

	us := h.classify(h.item("Euphoria.US.S01E01.1080p.mkv", 3300).ID)
	if got := h.tv.searched(); strings.Join(got, "|") != "euphoria us|euphoria" {
		t.Fatalf("searches = %q, want the country code dropped on a miss", got)
	}
	show := h.show(*us.ShowID)
	if show.TVmazeID == nil || *show.TVmazeID != 2 || show.NeedsCheck {
		t.Fatalf("show = %+v, want confident Euphoria", show)
	}

	dated := h.classify(h.item("Euphoria.2019.S01E02.720p.mkv", 3300).ID)
	if *dated.ShowID != show.ID || len(h.shows()) != 1 {
		t.Fatalf("2019 file joined show %d, want %d (one Euphoria)", *dated.ShowID, show.ID)
	}
	lanterns := h.classify(h.item("Lanterns.2025.S01E01.mkv", 3300).ID)
	if got := h.tv.searched(); strings.Join(got[2:], "|") != "euphoria 2019|euphoria|lanterns 2025|lanterns" {
		t.Fatalf("searches = %q, want the year dropped on a miss", got)
	}
	if ls := h.show(*lanterns.ShowID); ls.TVmazeID == nil || *ls.TVmazeID != 3 || ls.NeedsCheck {
		t.Fatalf("lanterns = %+v", ls)
	}
	if a := h.alias("euphoria us"); a.ShowID != show.ID {
		t.Fatalf("alias = %+v", a)
	}
}

func TestYearTieBreak(t *testing.T) {
	old := tvShow{id: 10, name: "Dark Matter", premiered: "2015-06-12"}
	recent := tvShow{id: 11, name: "Dark Matter", premiered: "2024-05-08"}

	h := newHarness(t)
	h.tv.add(old, "dark matter 2024", "dark matter")
	h.tv.add(recent, "dark matter 2024", "dark matter")
	item := h.classify(h.item("Dark.Matter.2024.S01E01.mkv", 3000).ID)
	show := h.show(*item.ShowID)
	if show.TVmazeID == nil || *show.TVmazeID != 11 || show.NeedsCheck || *show.Year != 2024 {
		t.Fatalf("show = %+v, want the 2024 Dark Matter, unflagged", show)
	}

	h = newHarness(t)
	h.tv.add(old, "dark matter")
	h.tv.add(recent, "dark matter")
	item = h.classify(h.item("Dark.Matter.S01E02.mkv", 3000).ID)
	show = h.show(*item.ShowID)
	if show.TVmazeID != nil || !show.NeedsCheck || show.Title != "Dark Matter" {
		t.Fatalf("show = %+v, want a flagged unmatched show when no year breaks the tie", show)
	}
}

func TestWeakResultNeverAttached(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 5, name: "Monster", premiered: "2004-04-07"}, "monster 2022", "monster")
	anime, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Monster", Year: ptr(2004), TVmazeID: ptr(int64(5))})
	if err != nil {
		t.Fatal(err)
	}
	item := h.classify(h.item("Monster.2022.S01E01.mkv", 3000).ID)
	if *item.ShowID == anime.ID {
		t.Fatal("weak result attached to the local show that has its tvmaze_id (M11)")
	}
	show := h.show(*item.ShowID)
	if show.TVmazeID != nil || !show.NeedsCheck || show.Title != "Monster" || *show.Year != 2022 || show.RefreshedAt == nil {
		t.Fatalf("show = %+v, want a settled, flagged, unmatched Monster (2022)", show)
	}
	if len(h.sched.all()) != 0 {
		t.Fatalf("a settled no-match queued refreshes: %+v", h.sched.all())
	}
}

func TestConfidentNewShowUnflagged(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	item := h.classify(h.item("Andor.S01E02.1080p.mkv", 2400).ID)
	show := h.show(*item.ShowID)
	if show.Title != "Andor" || *show.Year != 2022 || show.NeedsCheck || *show.Summary != "The rebel years." {
		t.Fatalf("show = %+v", show)
	}
	if show.PosterAt == nil || show.BackdropAt == nil || show.RefreshedAt == nil {
		t.Fatalf("show art/refresh not stamped: %+v", show)
	}
	if !exists(h.svc.PosterPath(show.ID)) || !exists(h.svc.BackdropPath(show.ID)) {
		t.Fatal("poster or backdrop not cached")
	}
	eps, err := h.st.ListShowEpisodes(h.ctx, show.ID)
	if err != nil || len(eps) != 11 {
		t.Fatalf("episodes = %d, %v", len(eps), err)
	}
	if item.Title != "Chapter 2" || item.NeedsCheck || item.Locked || *item.ShowKey != "andor" {
		t.Fatalf("item = %+v", item)
	}
	if !showEvent(h.drain(), events.ShowAdded, show.ID) {
		t.Fatal("no show.added")
	}
}

func TestEpisodeTitleOrder(t *testing.T) {
	titles := map[episodeNumber]string{{1, 1}: "Kassa", {1, 2}: "That Would Be Me", {2, 1}: "One Year Later"}
	cases := []struct {
		name                    string
		offset, season, ep, end int
		parsed, want            string
	}{
		{"TVmaze title wins", 0, 1, 1, 0, "Parsed", "Kassa"},
		{"offset maps the file season", 1, 1, 1, 0, "", "One Year Later"},
		{"parsed title when TVmaze has none", 0, 1, 5, 0, "Parsed", "Parsed"},
		{"Episode N last", 0, 1, 5, 0, "", "Episode 5"},
		{"a range joins its titles", 0, 1, 1, 2, "", "Kassa / That Would Be Me"},
		{"an untitled range", 0, 3, 1, 2, "", "Episodes 1–2"},
		{"specials are never offset", 1, 0, 1, 0, "", "Episode 1"},
	}
	for _, tc := range cases {
		if got := episodeTitle(titles, tc.offset, tc.season, tc.ep, tc.end, tc.parsed); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTVmazeDownThenRecovered(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	h.tv.down.Store(true)
	before := time.Now()
	item := h.classify(h.item("Andor.S01E03.1080p.mkv", 2400).ID)
	show := h.show(*item.ShowID)
	if show.TVmazeID != nil || !show.NeedsCheck || show.RefreshedAt != nil || item.Title != "Episode 3" {
		t.Fatalf("show = %+v item = %+v, want a local unmatched show", show, item)
	}
	runs := h.sched.all()
	if len(runs) != 1 || runs[0].showID != show.ID || runs[0].at.Before(before.Add(59*time.Minute)) {
		t.Fatalf("scheduled = %+v, want one refresh an hour out", runs)
	}

	if err := h.svc.RefreshShow(h.ctx, show.ID); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("refresh while down = %v, want ErrUnreachable", err)
	}

	h.tv.down.Store(false)
	if err := h.svc.RefreshShow(h.ctx, show.ID); err != nil {
		t.Fatal(err)
	}
	h.invariant()
	show = h.show(show.ID)
	if show.TVmazeID == nil || *show.TVmazeID != 1 || show.NeedsCheck || show.Title != "Andor" || show.PosterAt == nil {
		t.Fatalf("recovered show = %+v", show)
	}
	if got := h.get(item.ID); got.Title != "Chapter 3" {
		t.Fatalf("item title = %q, want TVmaze's", got.Title)
	}
}

func TestRefreshKeepsTitleLockedAndLockedItems(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	a := h.classify(h.item("Andor.S01E01.mkv", 2400).ID)
	b := h.classify(h.item("Andor.S01E02.mkv", 2400).ID)
	if _, err := h.svc.RenameShow(h.ctx, *a.ShowID, "Andor (mine)"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.EditItem(h.ctx, b.ID, ItemEdit{Title: ptr("My title")}); err != nil {
		t.Fatal(err)
	}
	h.tv.mu.Lock()
	show := h.tv.shows[1]
	show.name = "Andor (TVmaze rename)"
	show.episodes[0].title = "Kassa"
	show.episodes[1].title = "That Would Be Me"
	h.tv.shows[1] = show
	h.tv.mu.Unlock()

	if err := h.svc.RefreshShow(h.ctx, *a.ShowID); err != nil {
		t.Fatal(err)
	}
	if got := h.show(*a.ShowID); got.Title != "Andor (mine)" {
		t.Fatalf("title_locked title changed to %q", got.Title)
	}
	if got := h.get(a.ID); got.Title != "Kassa" {
		t.Fatalf("unlocked item title = %q, want the refreshed TVmaze title", got.Title)
	}
	if got := h.get(b.ID); got.Title != "My title" {
		t.Fatalf("locked item title = %q", got.Title)
	}
}

func TestAiringShowIDs(t *testing.T) {
	h := newHarness(t)
	today := time.Now()
	day := func(d int) string { return today.AddDate(0, 0, d).Format("2006-01-02") }
	airing := tvShow{id: 20, name: "Airing", premiered: "2026-01-01", episodes: []tvEpisode{{1, 1, "A", day(-80)}, {1, 2, "B", day(-10)}, {1, 3, "C", day(20)}}}
	ended := tvShow{id: 21, name: "Ended", premiered: "2020-01-01", episodes: []tvEpisode{{1, 1, "A", day(-400)}, {1, 2, "B", day(-61)}}}
	h.tv.add(airing, "airing")
	h.tv.add(ended, "ended")
	a := h.classify(h.item("Airing.S01E01.mkv", 1500).ID)
	h.classify(h.item("Ended.S01E01.mkv", 1500).ID)
	ids, err := h.svc.AiringShowIDs(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != *a.ShowID {
		t.Fatalf("airing = %v, want [%d]", ids, *a.ShowID)
	}
}

func TestLockedItemsUntouched(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	video := h.item("Andor.S01E04.mkv", 2400)
	locked := true
	if _, err := h.st.UpdateItem(h.ctx, video.ID, store.UpdateItemParams{
		Type: ptr("video"), Title: ptr("Keep me"), Locked: &locked, ShowID: store.SetNull[int64](),
	}); err != nil {
		t.Fatal(err)
	}
	got := h.classify(video.ID)
	if got.Title != "Keep me" || got.Type != "video" || got.ShowID != nil || got.ClassifiedAt == nil || !got.Locked {
		t.Fatalf("locked item = %+v", got)
	}
	if h.tv.hits.Load() != 0 || len(h.shows()) != 0 {
		t.Fatal("a locked non-episode reached TVmaze or formed a show")
	}
}

func TestBackfillLockHeuristic(t *testing.T) {
	h := newHarness(t, withClock(time.Hour))
	if err := h.svc.MarkLegacyCutoff(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.tv.add(andorShow(), "andor")
	legacy := func(relPath string, durationS float64, typ, title string) store.Item {
		return h.itemAs(relPath, durationS, store.NewItem{Type: typ, Title: title})
	}

	untouched := h.classify(legacy("Big.Buck.Bunny.(2008).mp4", 600, "video", "Big Buck Bunny").ID)
	if untouched.Locked || untouched.Type != "video" || *untouched.Year != 2008 {
		t.Fatalf("unedited video = %+v", untouched)
	}
	film := h.classify(legacy("Some.Film.2010.1080p.mkv", 600, "movie", "Some Film").ID)
	if !film.Locked || film.Type != "movie" || film.Title != "Some Film" {
		t.Fatalf("owner's type edit = %+v, want locked movie", film)
	}
	renamed := h.classify(legacy("Other.Film.2011.mkv", 7000, "video", "My Favourite Film").ID)
	if !renamed.Locked || renamed.Type != "video" || renamed.Title != "My Favourite Film" || renamed.ShowID != nil {
		t.Fatalf("owner's title edit = %+v, want it kept and locked", renamed)
	}
	if h.tv.hits.Load() != 0 {
		t.Fatal("locked non-episodes reached TVmaze")
	}

	episode := h.classify(legacy("Andor.S01E02.1080p.mkv", 2400, "episode", "Andor").ID)
	if episode.Locked || episode.Title != "Chapter 2" || episode.ShowID == nil || episode.NeedsCheck {
		t.Fatalf("unedited legacy episode = %+v, want reclassified", episode)
	}
	b1 := h.classify(legacy("Andor.S01E03.1080p.mkv", 2400, "episode", "The one with the prison").ID)
	if !b1.Locked || b1.Title != "The one with the prison" || !b1.NeedsCheck {
		t.Fatalf("renamed legacy episode = %+v, want locked, title kept, flagged", b1)
	}
	if b1.ShowID == nil || *b1.ShowID != *episode.ShowID || *b1.Season != 1 || *b1.Episode != 3 || *b1.ShowKey != "andor" {
		t.Fatalf("renamed legacy episode = %+v, want it in Andor as S1E3 (B1)", b1)
	}
}

func TestNewItemsSkipLockHeuristic(t *testing.T) {
	h := newHarness(t, withClock(-time.Hour))
	if err := h.svc.MarkLegacyCutoff(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.tv.add(andorShow(), "andor")
	item := h.classify(h.item("Andor.S01E01.1080p.WEB.mkv", 2400).ID)
	if item.Locked || item.Title != "Chapter 1" {
		t.Fatalf("new item = %+v; its v2 title differs from v1 (\"Andor WEB\") but it is not an owner edit", item)
	}
}

func TestEpisodeWithoutShowNameStaysPending(t *testing.T) {
	h := newHarness(t)
	item := h.classify(h.item("S01E01.mkv", 1500).ID)
	if item.ClassifiedAt != nil || !item.NeedsCheck || item.Type != "episode" || item.ShowID != nil {
		t.Fatalf("item = %+v, want a flagged pending episode", item)
	}
	moved, err := h.svc.MoveEpisode(h.ctx, item.ID, mustShow(t, h, "Somewhere").ID)
	if err != nil || moved.ClassifiedAt == nil {
		t.Fatalf("move = %+v, %v; want it filed", moved, err)
	}
}

func mustShow(t *testing.T, h *harness, title string) store.Show {
	t.Helper()
	show, err := h.st.CreateShow(h.ctx, store.NewShow{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return show
}

func TestSol(t *testing.T) {
	const unsure = "S01E05 The Ghost in the Shell Episodio 05 Phantom Fund (2026).mkv"
	ghost := tvShow{id: 7, name: "The Ghost in the Shell", premiered: "2026-07-01",
		episodes: []tvEpisode{{1, 5, "Phantom Fund", "2026-08-01"}}}
	episode := semantic.Answer{Kind: "episode", ShowTitle: "Ghost in the Shell", Season: ptr(1), Episode: ptr(5), Year: ptr(2026)}

	t.Run("confident answer replaces the parse", func(t *testing.T) {
		sol, fake := newFakeSol(t)
		h := newHarness(t, withSol(sol))
		h.setting(store.SettingSemanticClassification, "1")
		h.tv.add(tvShow{id: 8, name: "Ghost in the Shell", premiered: "2026-07-01"}, "ghost in the shell 2026")
		a := episode
		a.Confident = true
		fake.answer(a)
		item := h.classify(h.item(unsure, 1500).ID)
		show := h.show(*item.ShowID)
		if show.TVmazeID == nil || *show.TVmazeID != 8 || item.NeedsCheck || fake.callCount() != 1 {
			t.Fatalf("show = %+v item = %+v calls = %d", show, item, fake.callCount())
		}
		if *item.ShowKey != "the ghost in the shell" {
			t.Fatalf("item show_key = %q, want the parsed key kept for learning", *item.ShowKey)
		}
	})
	t.Run("unconfident answer keeps the parse and flags", func(t *testing.T) {
		sol, fake := newFakeSol(t)
		h := newHarness(t, withSol(sol))
		h.setting(store.SettingSemanticClassification, "1")
		h.tv.add(ghost, "the ghost in the shell 2026")
		fake.answer(episode)
		item := h.classify(h.item(unsure, 1500).ID)
		show := h.show(*item.ShowID)
		if *show.TVmazeID != 7 || !item.NeedsCheck || item.Title != "Phantom Fund" || fake.callCount() != 1 {
			t.Fatalf("show = %+v item = %+v", show, item)
		}
	})
	t.Run("failure keeps the parse and flags", func(t *testing.T) {
		sol, fake := newFakeSol(t)
		h := newHarness(t, withSol(sol))
		h.setting(store.SettingSemanticClassification, "1")
		h.tv.add(ghost, "the ghost in the shell 2026")
		fake.fail()
		item := h.classify(h.item(unsure, 1500).ID)
		if item.ShowID == nil || *h.show(*item.ShowID).TVmazeID != 7 || !item.NeedsCheck || fake.callCount() != 1 {
			t.Fatalf("item = %+v calls = %d", item, fake.callCount())
		}
	})
	t.Run("off: never asked, unsure parse flagged", func(t *testing.T) {
		sol, fake := newFakeSol(t)
		h := newHarness(t, withSol(sol))
		h.tv.add(ghost, "the ghost in the shell 2026")
		item := h.classify(h.item(unsure, 1500).ID)
		if fake.callCount() != 0 || !item.NeedsCheck {
			t.Fatalf("calls = %d item = %+v", fake.callCount(), item)
		}
	})
	t.Run("sure episodes are not asked", func(t *testing.T) {
		sol, fake := newFakeSol(t)
		h := newHarness(t, withSol(sol))
		h.setting(store.SettingSemanticClassification, "1")
		h.tv.add(andorShow(), "andor")
		item := h.classify(h.item("Andor.S01E01.mkv", 2400).ID)
		if fake.callCount() != 0 || item.NeedsCheck {
			t.Fatalf("calls = %d item = %+v", fake.callCount(), item)
		}
	})
	t.Run("a 30 to 60 minute video is asked", func(t *testing.T) {
		sol, fake := newFakeSol(t)
		h := newHarness(t, withSol(sol))
		h.setting(store.SettingSemanticClassification, "1")
		fake.answer(semantic.Answer{Kind: "movie", Title: "Some Documentary", Year: ptr(2019), Confident: true})
		item := h.classify(h.item("Some.Documentary.mkv", 45*60).ID)
		if fake.callCount() != 1 || item.Type != "movie" || *item.Year != 2019 || item.NeedsCheck {
			t.Fatalf("calls = %d item = %+v", fake.callCount(), item)
		}
	})
}

func TestApplyProbedDuration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clock time.Duration
		want  string
	}{
		{"a new video becomes a movie", -time.Hour, "movie"},
		{"a pre-upgrade video keeps what the old parser wrote", time.Hour, "video"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withClock(tc.clock))
			if err := h.svc.MarkLegacyCutoff(h.ctx); err != nil {
				t.Fatal(err)
			}
			item := h.item("Long.Film.2010.mkv", 0)
			if err := h.svc.ApplyProbedDuration(h.ctx, item, "Long.Film.2010.mkv", 7200); err != nil {
				t.Fatal(err)
			}
			if got := h.get(item.ID); got.Type != tc.want {
				t.Fatalf("type = %q, want %q", got.Type, tc.want)
			}
		})
	}
}
