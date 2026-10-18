package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

type fixture struct {
	t    *testing.T
	s    *store.Store
	ctx  context.Context
	root store.Root
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	root, err := s.UpsertRoot(ctx, "Media", "/Volumes/Media")
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	return &fixture{t: t, s: s, ctx: ctx, root: root}
}

// item creates a pending item with one online file of the given duration.
func (f *fixture) item(itemType, title string, durationS float64) store.Item {
	f.t.Helper()
	f.n++
	item, file, err := f.s.CreateItemWithFile(f.ctx,
		store.NewItem{Type: itemType, Title: title},
		store.NewFile{RootID: f.root.ID, RelPath: fmt.Sprintf("%d/%s.mkv", f.n, title), Size: 1, Mtime: time.Now(), Fingerprint: fmt.Sprint(f.n)})
	if err != nil {
		f.t.Fatalf("create %q: %v", title, err)
	}
	if err := f.s.UpdateFileProbe(f.ctx, file.ID, store.ProbeResult{Container: "mkv", DurationS: durationS}); err != nil {
		f.t.Fatalf("probe %q: %v", title, err)
	}
	f.invariant()
	return item
}

func (f *fixture) show(title string) store.Show {
	f.t.Helper()
	show, err := f.s.CreateShow(f.ctx, store.NewShow{Title: title, NeedsCheck: true})
	if err != nil {
		f.t.Fatalf("create show %q: %v", title, err)
	}
	return show
}

// episode creates a classified episode of show.
func (f *fixture) episode(show store.Show, season, episode int, title string) store.Item {
	f.t.Helper()
	item := f.item("episode", title, 3000)
	updated, err := f.s.UpdateItem(f.ctx, item.ID, store.UpdateItemParams{
		ShowID:       store.SetTo(show.ID),
		Season:       store.SetTo(season),
		Episode:      store.SetTo(episode),
		ClassifiedAt: store.SetTo(store.FormatTime(time.Now())),
	})
	if err != nil {
		f.t.Fatalf("classify %q: %v", title, err)
	}
	f.invariant()
	return updated
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.s.DB().ExecContext(f.ctx, query, args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

func (f *fixture) invariant() {
	f.t.Helper()
	bad, err := f.s.EpisodeInvariantViolations(f.ctx)
	if err != nil {
		f.t.Fatalf("invariant query: %v", err)
	}
	if len(bad) > 0 {
		f.t.Fatalf("items %v break the episode invariant", bad)
	}
}

func TestShowCRUD(t *testing.T) {
	f := newFixture(t)
	tvmaze := int64(42)
	year := 2022
	show, err := f.s.CreateShow(f.ctx, store.NewShow{Title: " Andor ", Year: &year, TVmazeID: &tvmaze, Network: ptr("Disney+")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if show.Title != "Andor" || show.NeedsCheck || show.SeasonOffset != 0 || *show.TVmazeID != 42 || *show.Network != "Disney+" {
		t.Fatalf("created show = %+v", show)
	}
	if _, err := f.s.CreateShow(f.ctx, store.NewShow{Title: "Andor again", TVmazeID: &tvmaze}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate tvmaze id: %v, want ErrConflict", err)
	}
	if _, err := f.s.CreateShow(f.ctx, store.NewShow{Title: "  "}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("blank title: %v, want ErrInvalidInput", err)
	}
	byTVmaze, err := f.s.GetShowByTVmazeID(f.ctx, 42)
	if err != nil || byTVmaze.ID != show.ID {
		t.Fatalf("by tvmaze id = %+v, %v", byTVmaze, err)
	}

	offset := 1
	poster := "2026-10-18 10:00:00"
	updated, err := f.s.UpdateShow(f.ctx, show.ID, store.UpdateShowParams{
		Title:        ptr("Star Wars: Andor"),
		TitleLocked:  ptr(true),
		SeasonOffset: &offset,
		PosterAt:     store.SetTo(poster),
		TVmazeID:     store.SetNull[int64](),
		Year:         store.SetNull[int](),
		Network:      store.SetTo("Apple TV"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != "Star Wars: Andor" || !updated.TitleLocked || updated.SeasonOffset != 1 ||
		updated.PosterAt == nil || *updated.PosterAt != poster || updated.TVmazeID != nil || updated.Year != nil ||
		*updated.Network != "Apple TV" {
		t.Fatalf("updated show = %+v", updated)
	}
	if _, err := f.s.UpdateShow(f.ctx, 999, store.UpdateShowParams{NeedsCheck: ptr(false)}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing show update: %v, want ErrNotFound", err)
	}
	shows, err := f.s.ListShows(f.ctx)
	if err != nil || len(shows) != 1 {
		t.Fatalf("list = %v, %v", shows, err)
	}
	if _, err := f.s.DeleteShow(f.ctx, show.ID); err != nil {
		t.Fatalf("delete empty show: %v", err)
	}
	if _, err := f.s.GetShow(f.ctx, show.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted show still readable: %v", err)
	}
}

func TestEpisodeInvariant(t *testing.T) {
	f := newFixture(t)
	show := f.show("Silo")
	now := store.FormatTime(time.Now())

	pending := f.item("episode", "Silo S01E01", 3000)
	if pending.ClassifiedAt != nil || pending.ShowID != nil {
		t.Fatalf("new item should be pending and showless: %+v", pending)
	}
	if _, err := f.s.UpdateItem(f.ctx, pending.ID, store.UpdateItemParams{Title: ptr("Freedom Day")}); err != nil {
		t.Fatalf("pending episode edit without a show: %v", err)
	}
	f.invariant()

	reject := func(name string, id int64, p store.UpdateItemParams) {
		t.Helper()
		if _, err := f.s.UpdateItem(f.ctx, id, p); !errors.Is(err, store.ErrInvalidInput) {
			t.Errorf("%s: %v, want ErrInvalidInput", name, err)
		}
		f.invariant()
	}
	reject("classify episode without show", pending.ID, store.UpdateItemParams{ClassifiedAt: store.SetTo(now)})
	reject("attach to a missing show", pending.ID, store.UpdateItemParams{ShowID: store.SetTo(int64(999))})

	ep, err := f.s.UpdateItem(f.ctx, pending.ID, store.UpdateItemParams{
		ShowID: store.SetTo(show.ID), Season: store.SetTo(1), Episode: store.SetTo(1),
		ShowKey: store.SetTo("silo"), Locked: ptr(true), NeedsCheck: ptr(true), ClassifiedAt: store.SetTo(now),
	})
	if err != nil {
		t.Fatalf("classify as episode: %v", err)
	}
	f.invariant()
	if *ep.ShowID != show.ID || *ep.Season != 1 || *ep.Episode != 1 || *ep.ShowKey != "silo" || !ep.Locked || !ep.NeedsCheck || ep.ClassifiedAt == nil {
		t.Fatalf("classified episode = %+v", ep)
	}

	reject("retype to movie keeping the show", ep.ID, store.UpdateItemParams{Type: ptr("movie")})
	reject("detach keeping type episode", ep.ID, store.UpdateItemParams{ShowID: store.SetNull[int64]()})
	if _, err := f.s.DeleteShow(f.ctx, show.ID); !errors.Is(err, store.ErrInvalidInput) {
		t.Errorf("deleting a show with classified episodes: %v, want ErrInvalidInput", err)
	}
	f.invariant()

	movie, err := f.s.UpdateItem(f.ctx, ep.ID, store.UpdateItemParams{Type: ptr("movie"), ShowID: store.SetNull[int64]()})
	if err != nil || movie.ShowID != nil || movie.Type != "movie" {
		t.Fatalf("detach to movie = %+v, %v", movie, err)
	}
	f.invariant()

	video := f.item("video", "Clip", 60)
	reject("classified video with a show", video.ID, store.UpdateItemParams{ShowID: store.SetTo(show.ID), ClassifiedAt: store.SetTo(now)})
	if _, err := f.s.UpdateItem(f.ctx, video.ID, store.UpdateItemParams{ClassifiedAt: store.SetTo(now)}); err != nil {
		t.Fatalf("classify video: %v", err)
	}
	f.invariant()
}

func TestShowAliasPrecedence(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.show("Euphoria"), f.show("Euphoria (US)"), f.show("Other")

	put := func(key string, show store.Show, source string) store.ShowAlias {
		t.Helper()
		alias, err := f.s.PutShowAlias(f.ctx, key, show.ID, source)
		if err != nil {
			t.Fatalf("put %s → %d (%s): %v", key, show.ID, source, err)
		}
		return alias
	}
	if got := put("euphoria us", a, store.AliasAuto); got.ShowID != a.ID || got.Source != store.AliasAuto {
		t.Fatalf("first auto = %+v", got)
	}
	if got := put("euphoria us", c, store.AliasAuto); got.ShowID != c.ID {
		t.Fatalf("auto over auto = %+v, want show %d", got, c.ID)
	}
	if got := put("euphoria us", b, store.AliasOwner); got.ShowID != b.ID || got.Source != store.AliasOwner {
		t.Fatalf("owner over auto = %+v", got)
	}
	if got := put("euphoria us", a, store.AliasAuto); got.ShowID != b.ID || got.Source != store.AliasOwner {
		t.Fatalf("auto overwrote owner: %+v", got)
	}
	if got := put("euphoria us", c, store.AliasOwner); got.ShowID != c.ID || got.Source != store.AliasOwner {
		t.Fatalf("owner over owner = %+v", got)
	}
	stored, err := f.s.GetShowAlias(f.ctx, "euphoria us")
	if err != nil || stored.ShowID != c.ID {
		t.Fatalf("get = %+v, %v", stored, err)
	}
	if _, err := f.s.GetShowAlias(f.ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing alias: %v", err)
	}
	if _, err := f.s.PutShowAlias(f.ctx, "x", a.ID, "sol"); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("bad source: %v", err)
	}
	if _, err := f.s.PutShowAlias(f.ctx, "x", 999, store.AliasAuto); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("missing show: %v", err)
	}
}

func TestMergeShow(t *testing.T) {
	f := newFixture(t)
	from, into := f.show("Vikingane"), f.show("Norsemen")
	e1 := f.episode(from, 1, 1, "E1")
	e2 := f.episode(from, 1, 2, "E2")
	e3 := f.episode(into, 1, 3, "E3")
	if err := f.s.MarkItemTrashed(f.ctx, e2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutShowAlias(f.ctx, "vikingane", from.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutShowAlias(f.ctx, "norsemen", from.ID, store.AliasOwner); err != nil {
		t.Fatal(err)
	}
	both, _ := f.s.CreateCollection(f.ctx, "both")
	onlyFrom, _ := f.s.CreateCollection(f.ctx, "only from")
	for _, step := range []error{
		f.s.AddShowToCollection(f.ctx, both.ID, from.ID),
		f.s.AddShowToCollection(f.ctx, both.ID, into.ID),
		f.s.AddShowToCollection(f.ctx, onlyFrom.ID, from.ID),
	} {
		if step != nil {
			t.Fatal(step)
		}
	}

	if _, err := f.s.MergeShow(f.ctx, from.ID, from.ID, false); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("self merge: %v", err)
	}
	if _, err := f.s.MergeShow(f.ctx, from.ID, 999, false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("merge into missing: %v", err)
	}
	removed, err := f.s.MergeShow(f.ctx, from.ID, into.ID, false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	f.invariant()
	if removed.ID != from.ID {
		t.Fatalf("removed = %+v", removed)
	}
	if _, err := f.s.GetShow(f.ctx, from.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("from survived: %v", err)
	}
	for _, id := range []int64{e1.ID, e2.ID, e3.ID} {
		item, _ := f.s.GetItem(f.ctx, id)
		if item.ShowID == nil || *item.ShowID != into.ID {
			t.Errorf("item %d show = %v, want %d", id, item.ShowID, into.ID)
		}
	}
	aliases, _ := f.s.ListShowAliases(f.ctx, into.ID)
	want := []store.ShowAlias{
		{Key: "norsemen", ShowID: into.ID, Source: store.AliasOwner},
		{Key: "vikingane", ShowID: into.ID, Source: store.AliasAuto},
	}
	if !reflect.DeepEqual(aliases, want) {
		t.Fatalf("aliases = %+v, want %+v", aliases, want)
	}
	summary, err := f.s.GetShowSummary(f.ctx, into.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summary.CollectionIDs, []int64{both.ID, onlyFrom.ID}) {
		t.Fatalf("collections after merge = %v", summary.CollectionIDs)
	}
	if summary.EpisodeCount != 2 {
		t.Fatalf("live episode count = %d, want 2", summary.EpisodeCount)
	}
}

func TestOwnerMergeMakesRepointedAliasesOwner(t *testing.T) {
	f := newFixture(t)
	from, into := f.show("Lioness"), f.show("Special Ops: Lioness")
	f.episode(from, 1, 1, "One")
	f.episode(into, 1, 2, "Two")
	if _, err := f.s.PutShowAlias(f.ctx, "lioness", from.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutShowAlias(f.ctx, "special ops lioness", into.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MergeShow(f.ctx, from.ID, into.ID, true); err != nil {
		t.Fatal(err)
	}
	aliases, _ := f.s.ListShowAliases(f.ctx, into.ID)
	want := []store.ShowAlias{
		{Key: "lioness", ShowID: into.ID, Source: store.AliasOwner},
		{Key: "special ops lioness", ShowID: into.ID, Source: store.AliasAuto},
	}
	if !reflect.DeepEqual(aliases, want) {
		t.Fatalf("aliases = %+v, want %+v", aliases, want)
	}
	ownerAlias, lockedItem, err := f.s.ShowOwnerMarks(f.ctx, into.ID)
	if err != nil || !ownerAlias || lockedItem {
		t.Fatalf("ShowOwnerMarks = %v, %v, %v; want an owner alias and no locked item", ownerAlias, lockedItem, err)
	}
}

func TestHardDeleteLastItemRemovesShow(t *testing.T) {
	f := newFixture(t)
	show := f.show("Carrie")
	e1 := f.episode(show, 1, 1, "E1")
	e2 := f.episode(show, 1, 2, "E2")
	if _, err := f.s.PutShowAlias(f.ctx, "carrie", show.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReplaceShowEpisodes(f.ctx, show.ID, []store.ShowEpisode{{Season: 1, Number: 1, Title: "Pilot"}}); err != nil {
		t.Fatal(err)
	}

	removed, err := f.s.HardDeleteItem(f.ctx, e1.ID)
	if err != nil || removed != nil {
		t.Fatalf("first purge removed %+v, %v; want show kept", removed, err)
	}
	f.invariant()
	removed, err = f.s.HardDeleteItem(f.ctx, e2.ID)
	if err != nil || removed == nil || removed.ID != show.ID {
		t.Fatalf("last purge removed %+v, %v; want show %d", removed, err, show.ID)
	}
	f.invariant()
	if _, err := f.s.GetShow(f.ctx, show.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("show survived its last item: %v", err)
	}
	if _, err := f.s.GetShowAlias(f.ctx, "carrie"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("alias survived its show: %v", err)
	}
	if eps, _ := f.s.ListShowEpisodes(f.ctx, show.ID); len(eps) != 0 {
		t.Fatalf("show_episodes survived: %v", eps)
	}
	if _, err := f.s.HardDeleteItem(f.ctx, e2.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purging twice: %v", err)
	}
	loose := f.item("video", "Loose", 60)
	if removed, err := f.s.HardDeleteItem(f.ctx, loose.ID); err != nil || removed != nil {
		t.Fatalf("showless purge = %+v, %v", removed, err)
	}
}

func TestReplaceShowEpisodes(t *testing.T) {
	f := newFixture(t)
	show := f.show("Furious")
	airdate, runtime := "2026-08-03", 50
	first := []store.ShowEpisode{
		{Season: 1, Number: 2, Title: "Two"},
		{Season: 1, Number: 1, Title: "One", Airdate: &airdate, RuntimeMin: &runtime},
	}
	if err := f.s.ReplaceShowEpisodes(f.ctx, show.ID, first); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.ListShowEpisodes(f.ctx, show.ID)
	if len(got) != 2 || got[0].Title != "One" || *got[0].Airdate != airdate || *got[0].RuntimeMin != 50 || got[1].Airdate != nil {
		t.Fatalf("episodes = %+v", got)
	}
	if err := f.s.ReplaceShowEpisodes(f.ctx, show.ID, []store.ShowEpisode{{Season: 2, Number: 1, Title: "S2"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = f.s.ListShowEpisodes(f.ctx, show.ID)
	if len(got) != 1 || got[0].Season != 2 {
		t.Fatalf("replace kept old rows: %+v", got)
	}
	if err := f.s.ReplaceShowEpisodes(f.ctx, 999, nil); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("missing show: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestDeleteShowIfEmpty(t *testing.T) {
	f := newFixture(t)
	show := f.show("Andor")
	e := f.episode(show, 1, 1, "E1")
	if removed, err := f.s.DeleteShowIfEmpty(f.ctx, show.ID); err != nil || removed != nil {
		t.Fatalf("show with an item removed: %+v, %v", removed, err)
	}
	if err := f.s.MarkItemTrashed(f.ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if removed, err := f.s.DeleteShowIfEmpty(f.ctx, show.ID); err != nil || removed != nil {
		t.Fatalf("show with a trashed item removed: %+v, %v", removed, err)
	}
	video := "video"
	if _, err := f.s.UpdateItem(f.ctx, e.ID, store.UpdateItemParams{Type: &video, ShowID: store.SetNull[int64]()}); err != nil {
		t.Fatal(err)
	}
	removed, err := f.s.DeleteShowIfEmpty(f.ctx, show.ID)
	if err != nil || removed == nil || removed.ID != show.ID {
		t.Fatalf("empty show = %+v, %v; want it removed", removed, err)
	}
	if removed, err := f.s.DeleteShowIfEmpty(f.ctx, show.ID); err != nil || removed != nil {
		t.Fatalf("missing show = %+v, %v", removed, err)
	}
}

func TestListAiringShowIDs(t *testing.T) {
	f := newFixture(t)
	day := func(s string) *string { return &s }
	shows := map[string][]store.ShowEpisode{
		"airing":   {{Season: 1, Number: 1, Title: "A", Airdate: day("2026-07-01")}, {Season: 1, Number: 2, Title: "B", Airdate: day("2026-10-01")}},
		"upcoming": {{Season: 1, Number: 1, Title: "A", Airdate: day("2026-01-01")}, {Season: 2, Number: 1, Title: "B", Airdate: day("2026-11-01")}},
		"ended":    {{Season: 1, Number: 1, Title: "A", Airdate: day("2026-08-01")}},
		"undated":  {{Season: 1, Number: 1, Title: "A"}},
	}
	ids := map[string]int64{}
	for i, name := range []string{"airing", "upcoming", "ended", "undated"} {
		tv := int64(100 + i)
		show, err := f.s.CreateShow(f.ctx, store.NewShow{Title: name, TVmazeID: &tv})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.s.ReplaceShowEpisodes(f.ctx, show.ID, shows[name]); err != nil {
			t.Fatal(err)
		}
		ids[name] = show.ID
	}
	unmatched := f.show("unmatched")
	if err := f.s.ReplaceShowEpisodes(f.ctx, unmatched.ID, shows["airing"]); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.ListAiringShowIDs(f.ctx, "2026-08-19", "2026-10-18")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{ids["airing"]}) {
		t.Fatalf("airing = %v, want [%d]", got, ids["airing"])
	}
}

func TestClassifyBookkeeping(t *testing.T) {
	f := newFixture(t)
	pending := f.item("episode", "Pending", 60)
	trashed := f.item("video", "Trashed", 60)
	if err := f.s.MarkItemTrashed(f.ctx, trashed.ID); err != nil {
		t.Fatal(err)
	}
	show := f.show("Done")
	done := f.episode(show, 1, 1, "Done")
	ids, err := f.s.ListUnclassifiedItemIDs(f.ctx)
	if err != nil || !reflect.DeepEqual(ids, []int64{pending.ID}) {
		t.Fatalf("unclassified = %v, %v", ids, err)
	}
	items, err := f.s.ListItemsByShow(f.ctx, show.ID)
	if err != nil || len(items) != 1 || items[0].ID != done.ID {
		t.Fatalf("show items = %+v, %v", items, err)
	}

	first, err := f.s.SetSettingIfAbsent(f.ctx, store.SettingClassifyLegacyBefore, "2026-10-18 00:00:00")
	if err != nil || first != "2026-10-18 00:00:00" {
		t.Fatalf("first = %q, %v", first, err)
	}
	again, err := f.s.SetSettingIfAbsent(f.ctx, store.SettingClassifyLegacyBefore, "2027-01-01 00:00:00")
	if err != nil || again != first {
		t.Fatalf("second write = %q, %v; want the first value kept", again, err)
	}
}
