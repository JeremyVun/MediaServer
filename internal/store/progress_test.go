package store_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

// earlier is before any progress saved during the test.
var earlier = store.FormatTime(time.Now().Add(-time.Hour))

func (f *fixture) stored(item store.Item) store.Progress {
	f.t.Helper()
	p, err := f.s.GetProgress(f.ctx, item.ID)
	if err != nil || p == nil {
		f.t.Fatalf("progress of %d: %v, %v", item.ID, p, err)
	}
	return *p
}

func TestMovingOnCompletesEpisode(t *testing.T) {
	f := newFixture(t)
	eden := f.show("East of Eden")
	e1, e2 := f.episode(eden, 1, 1, "Brothers"), f.episode(eden, 1, 2, "Cathy")
	f.progress(e1, 2730, 3000, false, earlier)

	_, movedOn, err := f.s.UpsertProgress(f.ctx, e2.ID, 60, 3000, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []store.MovedOn{{ItemID: e1.ID, ShowID: eden.ID}}; !reflect.DeepEqual(movedOn, want) {
		t.Fatalf("moved on = %+v, want %+v", movedOn, want)
	}
	if p := f.stored(e1); !p.Completed || p.UpdatedAt != earlier || p.PositionS != 2730 {
		t.Fatalf("E1 = %+v, want completed with updated_at kept", p)
	}
	if p := f.stored(e2); p.Completed {
		t.Fatalf("E2 = %+v, want still in progress", p)
	}
}

func TestMovingOnLeavesOthersInProgress(t *testing.T) {
	cases := []struct {
		name string
		// first gets 91 % progress (or position) at earlier; then the second is saved now.
		first, second   [2]int // season, episode
		position        float64
		firstIsLaterRow bool // first's row is newer than second's save
	}{
		{name: "below 80 percent", first: [2]int{1, 1}, second: [2]int{1, 2}, position: 2370},
		{name: "an earlier episode getting progress", first: [2]int{1, 2}, second: [2]int{1, 1}, position: 2730},
		{name: "a special does not move the main sequence on", first: [2]int{1, 8}, second: [2]int{0, 1}, position: 2730},
		{name: "the main sequence does not move a special on", first: [2]int{0, 1}, second: [2]int{1, 1}, position: 2730},
		{name: "a later episode with older progress", first: [2]int{1, 1}, second: [2]int{1, 2}, position: 2730, firstIsLaterRow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			show := f.show("Show")
			first := f.episode(show, tc.first[0], tc.first[1], "first")
			second := f.episode(show, tc.second[0], tc.second[1], "second")
			if tc.firstIsLaterRow {
				f.progress(second, 600, 3000, false, earlier)
				if _, _, err := f.s.UpsertProgress(f.ctx, first.ID, tc.position, 3000, false); err != nil {
					t.Fatal(err)
				}
			} else {
				f.progress(first, tc.position, 3000, false, earlier)
				if _, _, err := f.s.UpsertProgress(f.ctx, second.ID, 60, 3000, false); err != nil {
					t.Fatal(err)
				}
			}
			if p := f.stored(first); p.Completed {
				t.Fatalf("first = %+v, want still in progress", p)
			}
		})
	}
}

func TestMovingOnCrossesSeasonsNotShows(t *testing.T) {
	f := newFixture(t)
	a, b := f.show("A"), f.show("B")
	s1e8, s2e1 := f.episode(a, 1, 8, "finale"), f.episode(a, 2, 1, "premiere")
	other := f.episode(b, 1, 1, "other")
	f.progress(s1e8, 2730, 3000, false, earlier)
	f.progress(other, 2730, 3000, false, earlier)
	if _, _, err := f.s.UpsertProgress(f.ctx, s2e1.ID, 60, 3000, false); err != nil {
		t.Fatal(err)
	}
	if !f.stored(s1e8).Completed {
		t.Fatal("S1E8 not completed by moving on to S2E1")
	}
	if f.stored(other).Completed {
		t.Fatal("another show's episode was completed")
	}
}

func TestCompleteMovedOnEpisodesAtBoot(t *testing.T) {
	f := newFixture(t)
	eden, solo := f.show("East of Eden"), f.show("Solo")
	e1, e2, e3 := f.episode(eden, 1, 1, "Brothers"), f.episode(eden, 1, 2, "Cathy"), f.episode(eden, 1, 3, "In the Valley")
	e5, e6 := f.episode(eden, 1, 5, "Departure"), f.episode(eden, 1, 6, "The Great Try")
	alone := f.episode(solo, 1, 1, "Alone")
	f.progress(e1, 2760, 3000, false, "2026-10-19 10:00:00")
	f.progress(e2, 2790, 3000, false, "2026-10-19 11:00:00")
	f.progress(e3, 2730, 3000, false, "2026-10-19 12:00:00")
	f.progress(e5, 3000, 3000, true, "2026-10-19 13:00:00")
	f.progress(e6, 17, 3000, false, "2026-10-19 14:00:00")
	f.progress(alone, 2900, 3100, false, "2026-10-19 10:00:00")

	movedOn, err := f.s.CompleteMovedOnEpisodes(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.MovedOn{{e1.ID, eden.ID}, {e2.ID, eden.ID}, {e3.ID, eden.ID}}
	if !reflect.DeepEqual(movedOn, want) {
		t.Fatalf("moved on = %+v, want %+v", movedOn, want)
	}
	for item, at := range map[store.Item]string{e1: "2026-10-19 10:00:00", e2: "2026-10-19 11:00:00", e3: "2026-10-19 12:00:00"} {
		if p := f.stored(item); !p.Completed || p.UpdatedAt != at {
			t.Errorf("%s = %+v, want completed at its own updated_at", item.Title, p)
		}
	}
	if f.stored(e6).Completed || f.stored(alone).Completed {
		t.Fatal("E6 at 17 s or a show's only episode was completed")
	}
	if again, err := f.s.CompleteMovedOnEpisodes(f.ctx); err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %+v, %v; want nothing", again, err)
	}
}
