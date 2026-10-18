package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestMissingSinceFollowsStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root, _ := s.UpsertRoot(ctx, "A", "/Volumes/A")
	item, err := s.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.CreateFile(ctx, store.NewFile{ItemID: item.ID, RootID: root.ID, RelPath: "m.mkv", Size: 1, Mtime: time.Now(), Fingerprint: "f"})
	if err != nil {
		t.Fatal(err)
	}
	since := func() *string {
		t.Helper()
		var v *string
		if err := s.DB().QueryRowContext(ctx, `SELECT missing_since FROM media_files WHERE id = ?`, f.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := since(); v != nil {
		t.Fatalf("new file missing_since = %q", *v)
	}
	if err := s.SetFileStatus(ctx, f.ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if since() == nil {
		t.Fatal("missing_since not set when the file went missing")
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE media_files SET missing_since = '2026-01-01 00:00:00' WHERE id = ?`, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFileStatus(ctx, f.ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if v := since(); v == nil || *v != "2026-01-01 00:00:00" {
		t.Fatalf("re-marking missing moved missing_since to %v", v)
	}
	if _, err := s.SetRootFilesStatus(ctx, root.ID, "offline"); err != nil {
		t.Fatal(err)
	}
	if v := since(); v != nil {
		t.Fatalf("offline file kept missing_since %q", *v)
	}
}

func TestRootGoingOfflineKeepsTrashedFilesTrashed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root, _ := s.UpsertRoot(ctx, "A", "/Volumes/A")
	files := map[string]int64{}
	for _, name := range []string{"live", "trashed"} {
		item, f, err := s.CreateItemWithFile(ctx, store.NewItem{Title: name}, store.NewFile{
			RootID: root.ID, RelPath: name + ".mkv", Size: 1, Mtime: time.Now(), Fingerprint: name,
		})
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f.ID
		if name == "trashed" {
			if err := s.MarkItemTrashed(ctx, item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n, err := s.SetRootFilesStatus(ctx, root.ID, "offline"); err != nil || n != 1 {
		t.Fatalf("offline touched %d files (%v), want 1", n, err)
	}
	if err := s.DetachRoot(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"live": "offline", "trashed": "trashed"} {
		got, err := s.GetFile(ctx, files[name])
		if err != nil || got.Status != want {
			t.Fatalf("%s file status %q (%v), want %q", name, got.Status, err, want)
		}
	}
}

func TestTrashMissingItemRechecksItsFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root, _ := s.UpsertRoot(ctx, "A", "/Volumes/A")
	item, f, err := s.CreateItemWithFile(ctx, store.NewItem{Title: "Two files"}, store.NewFile{
		RootID: root.ID, RelPath: "a.mkv", Size: 1, Mtime: time.Now(), Fingerprint: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateFile(ctx, store.NewFile{ItemID: item.ID, RootID: root.ID, RelPath: "b.mkv", Size: 1, Mtime: time.Now(), Fingerprint: "b"})
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := s.SetFileStatus(ctx, f.ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TrashMissingItem(ctx, item.ID, later); err != nil || ok {
		t.Fatalf("trashed with one file online: %v %v", ok, err)
	}
	if err := s.SetFileStatus(ctx, g.ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TrashMissingItem(ctx, item.ID, time.Now().Add(-time.Minute)); err != nil || ok {
		t.Fatalf("trashed inside the grace: %v %v", ok, err)
	}
	if items, err := s.ListItemsMissingSince(ctx, later); err != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("candidates = %+v %v", items, err)
	}
	if ok, err := s.TrashMissingItem(ctx, item.ID, later); err != nil || !ok {
		t.Fatalf("not trashed with both files missing past the cutoff: %v %v", ok, err)
	}
	if ok, err := s.TrashMissingItem(ctx, item.ID, later); err != nil || ok {
		t.Fatalf("trashed twice: %v %v", ok, err)
	}
	for _, id := range []int64{f.ID, g.ID} {
		if got, _ := s.GetFile(ctx, id); got.Status != "missing" {
			t.Fatalf("file %d status %q, want left missing", id, got.Status)
		}
	}
}
