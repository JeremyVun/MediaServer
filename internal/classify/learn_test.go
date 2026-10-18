package classify

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

// twoAndors files a Star Wars Andor episode under its own unmatched show
// and an Andor episode under the matched Andor (ruling M11's setup).
func twoAndors(t *testing.T, h *harness) (stray, andor store.Item) {
	t.Helper()
	h.tv.add(andorShow(), "andor")
	stray = h.classify(h.item("Star.Wars.Andor.S01E01.mkv", 2400).ID)
	andor = h.classify(h.item("Andor.S01E02.mkv", 2400).ID)
	if *stray.ShowID == *andor.ShowID {
		t.Fatal("setup: Star Wars Andor joined Andor")
	}
	return stray, andor
}

func fakeArt(t *testing.T, h *harness, showID int64) {
	t.Helper()
	for _, p := range []string{h.svc.PosterPath(showID), h.svc.BackdropPath(showID)} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("art"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertShowGone(t *testing.T, h *harness, showID int64) {
	t.Helper()
	if _, err := h.st.GetShow(h.ctx, showID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("show %d still exists (%v)", showID, err)
	}
	if exists(h.svc.PosterPath(showID)) || exists(h.svc.BackdropPath(showID)) {
		t.Fatalf("show %d art left behind", showID)
	}
	if !showEvent(h.drain(), events.ShowRemoved, showID) {
		t.Fatalf("no show.removed for %d", showID)
	}
}

func TestMoveEpisodeTeachesOwnerAlias(t *testing.T) {
	h := newHarness(t)
	stray, andor := twoAndors(t, h)
	strayShow := *stray.ShowID
	fakeArt(t, h, strayShow)
	h.drain()

	moved, err := h.svc.MoveEpisode(h.ctx, stray.ID, *andor.ShowID)
	if err != nil {
		t.Fatal(err)
	}
	h.invariant()
	if *moved.ShowID != *andor.ShowID || !moved.Locked || moved.NeedsCheck || moved.Title != "Chapter 1" {
		t.Fatalf("moved = %+v, want locked in Andor with its TVmaze title", moved)
	}
	if a := h.alias("star wars andor"); a.ShowID != *andor.ShowID || a.Source != store.AliasOwner {
		t.Fatalf("alias = %+v, want owner alias to Andor", a)
	}
	assertShowGone(t, h, strayShow)

	later := h.classify(h.item("Star.Wars.Andor.S01E03.mkv", 2400).ID)
	if *later.ShowID != *andor.ShowID || later.Title != "Chapter 3" {
		t.Fatalf("later arrival = %+v, want it to follow the correction", later)
	}
}

func TestMoveKeepsOwnerTitle(t *testing.T) {
	h := newHarness(t)
	stray, andor := twoAndors(t, h)
	if _, err := h.svc.EditItem(h.ctx, stray.ID, ItemEdit{Title: ptr("Mine")}); err != nil {
		t.Fatal(err)
	}
	moved, err := h.svc.MoveEpisode(h.ctx, stray.ID, *andor.ShowID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Title != "Mine" {
		t.Fatalf("title = %q, want the owner's kept", moved.Title)
	}
}

func TestMergeShowsRepointsAliases(t *testing.T) {
	h := newHarness(t)
	stray, andor := twoAndors(t, h)
	from, into := *stray.ShowID, *andor.ShowID
	if _, err := h.st.PutShowAlias(h.ctx, "andor tv", from, store.AliasOwner); err != nil {
		t.Fatal(err)
	}
	fakeArt(t, h, from)
	h.drain()

	merged, err := h.svc.MergeShows(h.ctx, from, into)
	if err != nil {
		t.Fatal(err)
	}
	h.invariant()
	if merged.ID != into {
		t.Fatalf("merged = %+v", merged)
	}
	for key, source := range map[string]string{"star wars andor": store.AliasAuto, "andor tv": store.AliasOwner, "andor": store.AliasAuto} {
		if a := h.alias(key); a.ShowID != into || a.Source != source {
			t.Fatalf("alias %q = %+v, want %s alias to %d", key, a, source, into)
		}
	}
	evs := h.drain()
	if !itemEvent(evs, stray.ID) || !showEvent(evs, events.ShowUpdated, into) {
		t.Fatalf("events = %+v", evs)
	}
	if got := h.get(stray.ID); *got.ShowID != into || got.Title != "Chapter 1" {
		t.Fatalf("moved item = %+v, want it retitled by Andor's episodes", got)
	}
	if _, err := h.st.GetShow(h.ctx, from); !errors.Is(err, store.ErrNotFound) || exists(h.svc.PosterPath(from)) {
		t.Fatal("merged-away show or its art survived")
	}
	if !showEvent(evs, events.ShowRemoved, from) {
		t.Fatal("no show.removed for the merged-away show")
	}
}

func TestChangeMatchWithSeasonOffset(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 30, name: "Monsters", premiered: "2022-09-21", poster: true,
		episodes: []tvEpisode{{1, 1, "The Lyle and Erik Menendez Story", "2024-09-19"}}})
	item := h.classify(h.item("Monsters/Monsters.S02E01.mkv", 3000).ID)
	show := h.show(*item.ShowID)
	if show.TVmazeID != nil || !show.NeedsCheck {
		t.Fatalf("setup: show = %+v", show)
	}

	changed, err := h.svc.ChangeMatch(h.ctx, show.ID, Match{TVmazeID: ptr(int64(30)), SeasonOffset: ptr(-1)})
	if err != nil {
		t.Fatal(err)
	}
	if *changed.TVmazeID != 30 || changed.SeasonOffset != -1 || changed.NeedsCheck {
		t.Fatalf("changed = %+v", changed)
	}
	if a := h.alias("monsters"); a.ShowID != show.ID || a.Source != store.AliasAuto {
		t.Fatalf("alias changed: %+v", a)
	}
	runs := h.sched.all()
	if len(runs) != 1 || runs[0].showID != show.ID || time.Until(runs[0].at) > time.Second {
		t.Fatalf("scheduled = %+v, want an immediate refresh", runs)
	}
	if err := h.svc.RefreshShow(h.ctx, show.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.get(item.ID); got.Title != "The Lyle and Erik Menendez Story" {
		t.Fatalf("title = %q, want TVmaze S1E1 for file S2E1", got.Title)
	}
	if !exists(h.svc.PosterPath(show.ID)) {
		t.Fatal("poster not fetched by the refresh")
	}

	cleared, err := h.svc.ChangeMatch(h.ctx, show.ID, Match{})
	if err != nil {
		t.Fatal(err)
	}
	h.invariant()
	if cleared.TVmazeID != nil || cleared.NeedsCheck || cleared.RefreshedAt == nil || cleared.PosterAt != nil || cleared.SeasonOffset != 0 {
		t.Fatalf("cleared = %+v", cleared)
	}
	if exists(h.svc.PosterPath(show.ID)) {
		t.Fatal("old match's poster kept")
	}
	if got := h.get(item.ID); got.Title != "Episode 1" {
		t.Fatalf("title = %q, want the parsed fallback", got.Title)
	}
	if a := h.alias("monsters"); a.ShowID != show.ID {
		t.Fatalf("alias changed: %+v", a)
	}
	hits := h.tv.hits.Load()
	if err := h.svc.RefreshShow(h.ctx, show.ID); err != nil || h.tv.hits.Load() != hits {
		t.Fatalf("refresh of a show the owner unmatched = %v, %d requests", err, h.tv.hits.Load()-hits)
	}
}

func TestChangeMatchTaken(t *testing.T) {
	h := newHarness(t)
	stray, andor := twoAndors(t, h)
	_, err := h.svc.ChangeMatch(h.ctx, *stray.ShowID, Match{TVmazeID: ptr(int64(1))})
	var taken *MatchTakenError
	if !errors.As(err, &taken) || taken.ShowID != *andor.ShowID || !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want MatchTakenError for show %d", err, *andor.ShowID)
	}
}

func TestRenameAndConfirmShow(t *testing.T) {
	h := newHarness(t)
	stray, _ := twoAndors(t, h)
	renamed, err := h.svc.RenameShow(h.ctx, *stray.ShowID, "Andor extras")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Andor extras" || !renamed.TitleLocked || !renamed.NeedsCheck {
		t.Fatalf("renamed = %+v", renamed)
	}
	confirmed, err := h.svc.ConfirmShow(h.ctx, *stray.ShowID)
	if err != nil || confirmed.NeedsCheck {
		t.Fatalf("confirmed = %+v, %v", confirmed, err)
	}
	if a := h.alias("star wars andor"); a.ShowID != *stray.ShowID || a.Source != store.AliasAuto {
		t.Fatalf("alias changed: %+v", a)
	}
	if !showEvent(h.drain(), events.ShowUpdated, *stray.ShowID) {
		t.Fatal("no show.updated")
	}
	if _, err := h.svc.EditShow(h.ctx, *stray.ShowID, ShowEdit{}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("empty edit = %v", err)
	}
}

func TestEditItemFields(t *testing.T) {
	h := newHarness(t)
	_, andor := twoAndors(t, h)
	show := *andor.ShowID
	keys := h.alias("andor")

	flagged, err := h.st.UpdateItem(h.ctx, andor.ID, store.UpdateItemParams{NeedsCheck: ptr(true)})
	if err != nil || !flagged.NeedsCheck {
		t.Fatal(err)
	}
	confirmed, err := h.svc.ConfirmItem(h.ctx, andor.ID)
	if err != nil || confirmed.NeedsCheck || confirmed.Locked {
		t.Fatalf("confirmed = %+v, %v; want unflagged and unlocked", confirmed, err)
	}

	renumbered, err := h.svc.EditItem(h.ctx, andor.ID, ItemEdit{Episode: ptr(3)})
	if err != nil {
		t.Fatal(err)
	}
	if !renumbered.Locked || renumbered.NeedsCheck || *renumbered.Episode != 3 || renumbered.Title != "Chapter 3" {
		t.Fatalf("renumbered = %+v", renumbered)
	}
	if _, err := h.svc.EditItem(h.ctx, andor.ID, ItemEdit{Episode: ptr(0)}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("episode 0 = %v", err)
	}
	if a := h.alias("andor"); a != keys {
		t.Fatalf("field edits changed the alias: %+v, was %+v", a, keys)
	}

	fakeArt(t, h, show)
	h.drain()
	movie, err := h.svc.EditItem(h.ctx, andor.ID, ItemEdit{Type: ptr("movie")})
	if err != nil {
		t.Fatal(err)
	}
	h.invariant()
	if movie.Type != "movie" || movie.ShowID != nil || movie.Season != nil || !movie.Locked {
		t.Fatalf("movie = %+v, want detached", movie)
	}
	assertShowGone(t, h, show)

	if _, err := h.svc.EditItem(h.ctx, andor.ID, ItemEdit{Type: ptr("episode")}); !errors.Is(err, ErrEpisodeNeedsShow) || !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("episode without show = %v, want ErrEpisodeNeedsShow", err)
	}
	if _, err := h.svc.EditItem(h.ctx, andor.ID, ItemEdit{Season: ptr(1)}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("season on a movie = %v", err)
	}
	if _, err := h.svc.EditItem(h.ctx, andor.ID, ItemEdit{Type: ptr("video"), ShowID: &keys.ShowID}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("video with a show = %v", err)
	}
	target := mustShow(t, h, "Elsewhere")
	back, err := h.svc.EditItem(h.ctx, andor.ID, ItemEdit{Type: ptr("episode"), ShowID: &target.ID, Season: ptr(1), Episode: ptr(2)})
	if err != nil {
		t.Fatal(err)
	}
	h.invariant()
	if back.Type != "episode" || *back.ShowID != target.ID || *back.Episode != 2 {
		t.Fatalf("back = %+v", back)
	}
	if a := h.alias("andor"); a.ShowID != target.ID || a.Source != store.AliasOwner {
		t.Fatalf("alias = %+v, want the owner's move remembered", a)
	}
}

func TestEditPendingEpisodeTitleWaitsForClassify(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	item := h.item("Andor.S01E05.mkv", 2400)
	edited, err := h.svc.EditItem(h.ctx, item.ID, ItemEdit{Title: ptr("Owner title")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.ClassifiedAt != nil || !edited.Locked {
		t.Fatalf("edited = %+v, want a locked pending episode", edited)
	}
	got := h.classify(item.ID)
	if got.ShowID == nil || got.Title != "Owner title" || !got.NeedsCheck {
		t.Fatalf("classified = %+v, want it in Andor with the owner's title (B1)", got)
	}
}

func TestHardDeleteItemRemovesShowArt(t *testing.T) {
	h := newHarness(t)
	h.tv.add(andorShow(), "andor")
	item := h.classify(h.item("Andor.S01E01.mkv", 2400).ID)
	show := *item.ShowID
	if !exists(h.svc.PosterPath(show)) || !exists(h.svc.BackdropPath(show)) {
		t.Fatal("setup: art not cached")
	}
	h.drain()
	if err := h.svc.HardDeleteItem(h.ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	assertShowGone(t, h, show)
}
