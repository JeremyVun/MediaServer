package store_test

import (
	"reflect"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestDeriveMarkers(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	cases := []struct {
		name     string
		chapters []store.Chapter
		duration float64
		want     store.Markers
	}{
		{name: "no chapters", duration: 3000},
		{name: "unknown duration", chapters: []store.Chapter{{StartS: 2900, EndS: 3000, Title: "Credits"}}},
		{name: "intro and credits",
			chapters: []store.Chapter{
				{StartS: 0, EndS: 60, Title: "Recap"},
				{StartS: 60, EndS: 120, Title: " Intro "},
				{StartS: 120, EndS: 2800, Title: "Chapter 2"},
				{StartS: 2800, EndS: 3000, Title: "CREDITS"},
			},
			duration: 3000,
			want:     store.Markers{Intro: &store.Span{StartS: 60, EndS: 120}, CreditsStartS: f64(2800)}},
		{name: "every intro and credits spelling",
			chapters: []store.Chapter{{StartS: 10, EndS: 20, Title: "opening credits"}, {StartS: 2900, EndS: 3000, Title: "End Credits"}},
			duration: 3000,
			want:     store.Markers{Intro: &store.Span{StartS: 10, EndS: 20}, CreditsStartS: f64(2900)}},
		{name: "first matching credits chapter wins",
			chapters: []store.Chapter{{StartS: 2300, EndS: 2400, Title: "Outro"}, {StartS: 2900, EndS: 3000, Title: "ED"}},
			duration: 3000,
			want:     store.Markers{CreditsStartS: f64(2300)}},
		{name: "credits before the last quarter are ignored",
			chapters: []store.Chapter{{StartS: 2000, EndS: 2100, Title: "Credits"}, {StartS: 2950, EndS: 3000, Title: "Ending"}},
			duration: 3000,
			want:     store.Markers{CreditsStartS: f64(2950)}},
		{name: "intro after the first 30 percent is ignored",
			chapters: []store.Chapter{{StartS: 1000, EndS: 1100, Title: "OP"}},
			duration: 3000},
		{name: "M6 chapters past the duration are ignored",
			chapters: []store.Chapter{{StartS: 3600, EndS: 3700, Title: "Intro"}, {StartS: 3100, EndS: 3200, Title: "Credits"}},
			duration: 3000},
		{name: "intro end is clamped to the duration",
			chapters: []store.Chapter{{StartS: 5, EndS: 9000, Title: "Intro"}},
			duration: 30,
			want:     store.Markers{Intro: &store.Span{StartS: 5, EndS: 30}}},
		{name: "titles must match whole",
			chapters: []store.Chapter{{StartS: 10, EndS: 20, Title: "Intro to Andor"}, {StartS: 2900, EndS: 3000, Title: "Credits roll"}},
			duration: 3000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.DeriveMarkers(tc.chapters, tc.duration); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DeriveMarkers = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReplaceFileChapters(t *testing.T) {
	f := newFixture(t)
	f.item("video", "A", 3000)
	files, err := f.s.ListFilesNeedingChapters(f.ctx)
	if err != nil || len(files) != 1 {
		t.Fatalf("needing chapters = %v, %v", files, err)
	}
	chapters := []store.Chapter{{StartS: 0, EndS: 60, Title: "Intro"}, {StartS: 60, EndS: 3000}}
	if err := f.s.ReplaceFileChapters(f.ctx, files[0].ID, chapters); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.ListFileChapters(f.ctx, files[0].ID)
	if !reflect.DeepEqual(got, chapters) {
		t.Fatalf("chapters = %+v", got)
	}
	if left, _ := f.s.ListFilesNeedingChapters(f.ctx); len(left) != 0 {
		t.Fatalf("scanned file still listed: %v", left)
	}
	if err := f.s.ReplaceFileChapters(f.ctx, files[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.ListFileChapters(f.ctx, files[0].ID); len(got) != 0 {
		t.Fatalf("replace with none kept %v", got)
	}
	if err := f.s.ReplaceFileChapters(f.ctx, 999, nil); err == nil {
		t.Fatal("missing file accepted")
	}

	f.item("video", "B", 3000)
	if err := f.s.SetRootOnline(f.ctx, f.root.ID, false); err != nil {
		t.Fatal(err)
	}
	if left, _ := f.s.ListFilesNeedingChapters(f.ctx); len(left) != 0 {
		t.Fatalf("offline root's files listed: %v", left)
	}
}

func TestUpsertProgressCompletion(t *testing.T) {
	f := newFixture(t)
	plain := f.item("episode", "No chapters", 3000)
	withCredits := f.item("episode", "Credits at 92%", 3960)
	files, _ := f.s.ListFilesForItem(f.ctx, withCredits.ID)
	if err := f.s.ReplaceFileChapters(f.ctx, files[0].ID, []store.Chapter{
		{StartS: 0, EndS: 3640, Title: "Episode"}, {StartS: 3640, EndS: 3960, Title: "Credits"},
	}); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		name     string
		item     store.Item
		position float64
		duration float64
		explicit bool
		want     bool
	}{
		{"short of 95 percent", plain, 2700, 3000, false, false},
		{"95 percent", plain, 2850, 3000, false, true},
		{"rewatch from the start resets", plain, 30, 3000, false, false},
		{"explicit from the Next card", plain, 2000, 3000, true, true},
		{"before the credits", withCredits, 3600, 3960, false, false},
		{"past the credits marker", withCredits, 3650, 3960, false, true},
		{"rewatch resets despite credits", withCredits, 10, 3960, false, false},
	}
	for _, step := range steps {
		got, err := f.s.UpsertProgress(f.ctx, step.item.ID, step.position, step.duration, step.explicit)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		stored, _ := f.s.GetProgress(f.ctx, step.item.ID)
		if got.Completed != step.want || stored.Completed != step.want {
			t.Errorf("%s: completed returned %v stored %v, want %v", step.name, got.Completed, stored.Completed, step.want)
		}
		if got.UpdatedAt == "" || stored.UpdatedAt != got.UpdatedAt {
			t.Errorf("%s: updated_at returned %q stored %q", step.name, got.UpdatedAt, stored.UpdatedAt)
		}
	}

	// Ruling M5 backfill: a row stuck at the credits completes once the
	// file's chapters are known, without moving its timestamp.
	stuck := f.item("episode", "Below E1", 3000)
	stuckFiles, _ := f.s.ListFilesForItem(f.ctx, stuck.ID)
	if _, err := f.s.UpsertProgress(f.ctx, stuck.ID, 2700, 3000, false); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE watch_progress SET updated_at = '2026-08-01 20:00:00' WHERE item_id = ?`, stuck.ID)
	if _, changed, err := f.s.ReevaluateProgress(f.ctx, stuck.ID); err != nil || changed {
		t.Fatalf("re-evaluate without chapters changed=%v err=%v", changed, err)
	}
	if err := f.s.ReplaceFileChapters(f.ctx, stuckFiles[0].ID, []store.Chapter{{StartS: 2650, EndS: 3000, Title: "Credits"}}); err != nil {
		t.Fatal(err)
	}
	progress, changed, err := f.s.ReevaluateProgress(f.ctx, stuck.ID)
	if err != nil || !changed || !progress.Completed {
		t.Fatalf("re-evaluate = %+v changed=%v err=%v", progress, changed, err)
	}
	stored, _ := f.s.GetProgress(f.ctx, stuck.ID)
	if !stored.Completed || stored.UpdatedAt != "2026-08-01 20:00:00" {
		t.Fatalf("stored after re-evaluate = %+v", stored)
	}
	if progress, changed, err := f.s.ReevaluateProgress(f.ctx, plain.ID+1000); err != nil || changed || progress != nil {
		t.Fatalf("no progress row: %+v %v %v", progress, changed, err)
	}
}
