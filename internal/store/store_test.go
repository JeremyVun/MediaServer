package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(sqldb)
}

func TestRootUpsertIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r1, err := s.UpsertRoot(ctx, "Media A", "/Volumes/Media-A")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	r2, err := s.UpsertRoot(ctx, "Media A renamed", "/Volumes/Media-A")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if r1.ID != r2.ID {
		t.Errorf("upsert by same path created a new root: %d != %d", r1.ID, r2.ID)
	}
	if r2.Name != "Media A renamed" {
		t.Errorf("name not updated: %q", r2.Name)
	}

	roots, err := s.ListRoots(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("want 1 root, got %d", len(roots))
	}
}

func TestRootOnlineFlag(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r, _ := s.UpsertRoot(ctx, "A", "/Volumes/A")
	if !r.Online {
		t.Fatal("new root should default to online")
	}
	if err := s.SetRootOnline(ctx, r.ID, false); err != nil {
		t.Fatalf("set offline: %v", err)
	}
	r, _ = s.GetRoot(ctx, r.ID)
	if r.Online {
		t.Error("root still online after SetRootOnline(false)")
	}
	if err := s.SetRootOnline(ctx, 999, true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing root: got %v, want ErrNotFound", err)
	}
}

func TestRootDetachAndReattachPreservesCatalog(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	root, err := s.UpsertRoot(ctx, "Media A", "/Volumes/Media-A")
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	item, err := s.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := s.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movie.mp4",
		Size:        10,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	if err := s.DetachRoot(ctx, root.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	detached, err := s.GetRoot(ctx, root.ID)
	if err != nil {
		t.Fatalf("get detached: %v", err)
	}
	if detached.Attached || detached.Online {
		t.Fatalf("detached root = %+v", detached)
	}
	roots, err := s.ListRoots(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(roots) != 0 {
		t.Fatalf("active roots = %+v, want none", roots)
	}
	gotFile, err := s.GetFile(ctx, file.ID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if gotFile.Status != "offline" {
		t.Fatalf("file status = %q, want offline", gotFile.Status)
	}

	reattached, err := s.UpsertRoot(ctx, "Media A renamed", "/Volumes/Media-A")
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if reattached.ID != root.ID || !reattached.Attached {
		t.Fatalf("reattached root = %+v, original id %d", reattached, root.ID)
	}
	count, err := s.CountFilesForRoot(ctx, root.ID)
	if err != nil {
		t.Fatalf("count files: %v", err)
	}
	if count != 1 {
		t.Fatalf("file count = %d, want 1", count)
	}
}

func TestItemCRUDAndSoftDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	year := 2008
	item, err := s.CreateItem(ctx, store.NewItem{Title: "Big Buck Bunny", Year: &year})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.Type != "video" {
		t.Errorf("default type = %q, want video", item.Type)
	}

	newTitle := "Big Buck Bunny (remastered)"
	updated, err := s.UpdateItem(ctx, item.ID, store.UpdateItemParams{Title: &newTitle})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("title = %q", updated.Title)
	}
	if updated.Year == nil || *updated.Year != 2008 {
		t.Error("partial update clobbered year")
	}

	if err := s.SoftDeleteItem(ctx, item.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	got, _ := s.GetItem(ctx, item.ID)
	if got.DeletedAt == nil {
		t.Fatal("deleted_at not set")
	}
	_, total, _ := s.ListItemSummaries(ctx, store.ListItemsOpts{})
	if total != 0 {
		t.Errorf("trashed item still listed: total=%d", total)
	}
	trashed, total, _ := s.ListItemSummaries(ctx, store.ListItemsOpts{Trashed: true})
	if total != 1 || len(trashed) != 1 {
		t.Errorf("trash listing: total=%d len=%d", total, len(trashed))
	}

	if err := s.RestoreItem(ctx, item.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, _ = s.GetItem(ctx, item.ID)
	if got.DeletedAt != nil {
		t.Error("deleted_at still set after restore")
	}

	if _, err := s.GetItem(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing item: got %v, want ErrNotFound", err)
	}
}

func TestWatchProgressUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	progress, err := s.UpsertProgress(ctx, item.ID, 50, 100)
	if err != nil {
		t.Fatalf("insert progress: %v", err)
	}
	if progress.Completed {
		t.Fatal("50% progress should not be complete")
	}
	got, err := s.GetProgress(ctx, item.ID)
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if got == nil || got.PositionS != 50 || got.DurationS != 100 || got.Completed {
		t.Fatalf("progress = %+v", got)
	}

	progress, err = s.UpsertProgress(ctx, item.ID, 95, 100)
	if err != nil {
		t.Fatalf("update progress: %v", err)
	}
	if !progress.Completed {
		t.Fatal("95% progress should be complete")
	}
	got, err = s.GetProgress(ctx, item.ID)
	if err != nil {
		t.Fatalf("get updated progress: %v", err)
	}
	if got == nil || got.PositionS != 95 || got.DurationS != 100 || !got.Completed {
		t.Fatalf("updated progress = %+v", got)
	}
}

func TestListItemsSortAndPaging(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, title := range []string{"banana", "Apple", "cherry"} {
		if _, err := s.CreateItem(ctx, store.NewItem{Title: title}); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
	}

	items, total, err := s.ListItemSummaries(ctx, store.ListItemsOpts{Sort: "title"})
	if err != nil {
		t.Fatalf("list by title: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d", total)
	}
	gotOrder := []string{items[0].Title, items[1].Title, items[2].Title}
	wantOrder := []string{"Apple", "banana", "cherry"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("title sort order = %v, want %v", gotOrder, wantOrder)
		}
	}

	page, total, err := s.ListItemSummaries(ctx, store.ListItemsOpts{Sort: "title", Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if total != 3 || len(page) != 1 || page[0].Title != "banana" {
		t.Errorf("paging: total=%d len=%d first=%q", total, len(page), page[0].Title)
	}

	if _, _, err := s.ListItemSummaries(ctx, store.ListItemsOpts{Sort: "nope"}); err == nil {
		t.Error("bad sort accepted")
	}
}

func TestListItemsInProgressFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateItem(ctx, store.NewItem{Title: "unwatched"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	started, _ := s.CreateItem(ctx, store.NewItem{Title: "started"})
	finished, _ := s.CreateItem(ctx, store.NewItem{Title: "finished"})
	startedLater, _ := s.CreateItem(ctx, store.NewItem{Title: "started later"})

	if _, err := s.UpsertProgress(ctx, started.ID, 60, 600); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if _, err := s.UpsertProgress(ctx, finished.ID, 590, 600); err != nil { // ≥95% ⇒ completed
		t.Fatalf("progress: %v", err)
	}
	if _, err := s.UpsertProgress(ctx, startedLater.ID, 30, 600); err != nil {
		t.Fatalf("progress: %v", err)
	}

	items, total, err := s.ListItemSummaries(ctx, store.ListItemsOpts{InProgress: true, Sort: "watched"})
	if err != nil {
		t.Fatalf("list in progress: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("in-progress total=%d len=%d, want 2", total, len(items))
	}
	for _, it := range items {
		if it.Title != "started" && it.Title != "started later" {
			t.Errorf("unexpected item %q in in-progress list", it.Title)
		}
		if it.Progress == nil || it.Progress.Completed || it.Progress.PositionS <= 0 {
			t.Errorf("item %q has wrong progress %+v", it.Title, it.Progress)
		}
	}
}

func TestFileLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	root, _ := s.UpsertRoot(ctx, "A", "/Volumes/A")
	item, _ := s.CreateItem(ctx, store.NewItem{Title: "Movie"})

	mtime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	f, err := s.CreateFile(ctx, store.NewFile{
		ItemID: item.ID, RootID: root.ID, RelPath: "movies/movie.mkv",
		Size: 1000, Mtime: mtime, Fingerprint: "deadbeef",
	})
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if f.Status != "online" {
		t.Errorf("default status = %q", f.Status)
	}
	if f.Mtime != "2026-07-01 12:00:00" {
		t.Errorf("mtime stored as %q", f.Mtime)
	}

	// UNIQUE(root_id, rel_path)
	if _, err := s.CreateFile(ctx, store.NewFile{
		ItemID: item.ID, RootID: root.ID, RelPath: "movies/movie.mkv",
		Size: 1, Mtime: mtime, Fingerprint: "x",
	}); err == nil {
		t.Error("duplicate (root, rel_path) accepted")
	}

	// Probe update
	if err := s.UpdateFileProbe(ctx, f.ID, store.ProbeResult{
		Container: "matroska", DurationS: 596.4, Bitrate: 9_800_000, Width: 1920, Height: 1080,
	}); err != nil {
		t.Fatalf("probe update: %v", err)
	}
	f, _ = s.GetFile(ctx, f.ID)
	if f.Container == nil || *f.Container != "matroska" || f.ProbedAt == nil {
		t.Error("probe fields not persisted")
	}
	if f.DurationS == nil || *f.DurationS != 596.4 {
		t.Error("duration not persisted")
	}

	// Move detection path: fingerprint lookup + location update.
	matches, _ := s.GetFilesByFingerprint(ctx, "deadbeef")
	if len(matches) != 1 || matches[0].ID != f.ID {
		t.Fatalf("fingerprint lookup: %v", matches)
	}
	movedMtime := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	if err := s.RelocateFile(ctx, f.ID, root.ID, "moved/movie.mkv", 1000, movedMtime, "deadbeef"); err != nil {
		t.Fatalf("move: %v", err)
	}
	moved, _ := s.GetFileByLocation(ctx, root.ID, "moved/movie.mkv")
	if moved.ID != f.ID {
		t.Error("identity not preserved across move")
	}
	if moved.Status != "online" || moved.Mtime != "2026-07-02 09:00:00" {
		t.Errorf("relocate did not refresh stat/status: %+v", moved)
	}

	// Root offline transition.
	n, err := s.SetRootFilesStatus(ctx, root.ID, "offline")
	if err != nil || n != 1 {
		t.Fatalf("bulk status: n=%d err=%v", n, err)
	}
	f, _ = s.GetFile(ctx, f.ID)
	if f.Status != "offline" {
		t.Errorf("status = %q after root offline", f.Status)
	}

	// Cascade: deleting the item removes its files.
	if err := s.HardDeleteItem(ctx, item.ID); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	if _, err := s.GetFile(ctx, f.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("file survived item cascade: %v", err)
	}
}

func TestReplaceFileStreams(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	root, _ := s.UpsertRoot(ctx, "A", "/Volumes/A")
	item, _ := s.CreateItem(ctx, store.NewItem{Title: "Movie"})
	f, _ := s.CreateFile(ctx, store.NewFile{
		ItemID: item.ID, RootID: root.ID, RelPath: "m.mkv",
		Size: 1, Mtime: time.Now(), Fingerprint: "fp",
	})

	eng := "eng"
	ch := 6
	first := []store.Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
		{StreamIndex: 1, Kind: "audio", Codec: "dts", Lang: &eng, Channels: &ch, IsDefault: true},
	}
	if err := s.ReplaceFileStreams(ctx, f.ID, first); err != nil {
		t.Fatalf("replace: %v", err)
	}

	// Re-probe finds different streams; old rows must be gone.
	second := []store.Stream{
		{StreamIndex: 0, Kind: "video", Codec: "hevc"},
	}
	if err := s.ReplaceFileStreams(ctx, f.ID, second); err != nil {
		t.Fatalf("second replace: %v", err)
	}
	got, err := s.ListFileStreams(ctx, f.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Codec != "hevc" {
		t.Errorf("streams after replace: %+v", got)
	}
}

func TestQueueDepth(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	depth, err := s.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("queue depth: %v", err)
	}
	if depth != 0 {
		t.Errorf("empty queue depth = %d", depth)
	}

	for _, status := range []string{"queued", "running", "done", "failed"} {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO jobs (type, payload, status) VALUES ('probe', '{}', ?)`, status); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}
	depth, _ = s.QueueDepth(ctx)
	if depth != 2 {
		t.Errorf("queue depth = %d, want 2 (queued+running)", depth)
	}
}

func TestJobLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	job, err := s.EnqueueJob(ctx, "probe", `{"root_id":1}`)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if job.Status != "queued" || job.Attempts != 0 {
		t.Fatalf("new job = %+v", job)
	}

	claimed, err := s.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != job.ID || claimed.Status != "running" || claimed.StartedAt == nil {
		t.Fatalf("claimed = %+v", claimed)
	}
	if _, err := s.ClaimNextJob(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty claim = %v, want ErrNotFound", err)
	}

	n, err := s.ResetRunningJobs(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reset running n=%d err=%v", n, err)
	}
	claimed, err = s.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim after reset: %v", err)
	}
	if err := s.RescheduleJob(ctx, claimed.ID, 1, time.Now().Add(-time.Second), "try again"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	jobs, err := s.ListJobs(ctx, store.ListJobsOpts{Status: "queued"})
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Error == nil || *jobs[0].Error != "try again" {
		t.Fatalf("queued jobs = %+v", jobs)
	}

	claimed, err = s.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim rescheduled: %v", err)
	}
	if err := s.MarkJobFailed(ctx, claimed.ID, 2, "broken media"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	failed, err := s.GetJob(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if failed.Status != "failed" || failed.Attempts != 2 || failed.Error == nil {
		t.Fatalf("failed job = %+v", failed)
	}
}

func TestFTSTriggersStayInSync(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, _ := s.CreateItem(ctx, store.NewItem{Title: "Big Buck Bunny"})

	count := func() int {
		var n int
		if err := s.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH '"big"*'`).Scan(&n); err != nil {
			t.Fatalf("fts query: %v", err)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("insert trigger did not index title")
	}

	newTitle := "Sintel"
	if _, err := s.UpdateItem(ctx, item.ID, store.UpdateItemParams{Title: &newTitle}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if count() != 0 {
		t.Error("update trigger left stale FTS entry")
	}

	if err := s.HardDeleteItem(ctx, item.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH '"sintel"*'`).Scan(&n)
	if n != 0 {
		t.Error("delete trigger left stale FTS entry")
	}
}

func TestItemSummariesAndSearch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	root, _ := s.UpsertRoot(ctx, "A", t.TempDir())
	year := 2008
	item, _ := s.CreateItem(ctx, store.NewItem{Title: "Big Buck Bunny", Year: &year})
	file, err := s.CreateFile(ctx, store.NewFile{
		ItemID: item.ID, RootID: root.ID, RelPath: "bbb.mp4",
		Size: 10, Mtime: time.Now(), Fingerprint: "fp",
	})
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if err := s.UpdateFileProbe(ctx, file.ID, store.ProbeResult{
		Container: "mov", DurationS: 596.4, Bitrate: 1, Width: 640, Height: 360,
	}); err != nil {
		t.Fatalf("probe: %v", err)
	}

	items, total, err := s.ListItemSummaries(ctx, store.ListItemsOpts{})
	if err != nil {
		t.Fatalf("summaries: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len=%d", total, len(items))
	}
	if !items[0].Available || items[0].DurationS == nil || *items[0].DurationS != 596.4 {
		t.Fatalf("summary = %+v", items[0])
	}

	search, total, err := s.SearchItemSummaries(ctx, store.SearchItemsOpts{Query: "bun", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(search) != 1 || search[0].ID != item.ID {
		t.Fatalf("search total=%d items=%+v", total, search)
	}

	if q := store.FTS5PrefixQuery(`big; bun`); q != `"big"* "bun"*` {
		t.Fatalf("prefix query = %q", q)
	}
}

func TestEnqueueJobDedupesQueued(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.EnqueueJob(ctx, "probe", `{"root_id":1,"rel_path":"a.mkv"}`)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	dup, err := s.EnqueueJob(ctx, "probe", `{"root_id":1,"rel_path":"a.mkv"}`)
	if err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	if dup.ID != first.ID {
		t.Errorf("duplicate queued job created: %d != %d", dup.ID, first.ID)
	}
	other, err := s.EnqueueJob(ctx, "probe", `{"root_id":1,"rel_path":"b.mkv"}`)
	if err != nil {
		t.Fatalf("distinct enqueue: %v", err)
	}
	if other.ID == first.ID {
		t.Error("distinct payload deduped")
	}

	// A running job must not suppress a fresh enqueue — it may be working
	// on stale state.
	claimed, err := s.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != first.ID {
		t.Fatalf("claimed %d, want %d", claimed.ID, first.ID)
	}
	requeued, err := s.EnqueueJob(ctx, "probe", `{"root_id":1,"rel_path":"a.mkv"}`)
	if err != nil {
		t.Fatalf("re-enqueue while running: %v", err)
	}
	if requeued.ID == first.ID {
		t.Error("running job suppressed a fresh enqueue")
	}
}

func TestListItemSummariesCollectionFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	in, _ := s.CreateItem(ctx, store.NewItem{Title: "In Collection"})
	second, _ := s.CreateItem(ctx, store.NewItem{Title: "Second In Collection"})
	out, _ := s.CreateItem(ctx, store.NewItem{Title: "Not In Collection"})
	col, err := s.CreateCollection(ctx, "Favourites")
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if err := s.AddItemToCollection(ctx, col.ID, in.ID); err != nil {
		t.Fatalf("add item: %v", err)
	}
	if err := s.AddItemToCollection(ctx, col.ID, second.ID); err != nil {
		t.Fatalf("add second item: %v", err)
	}
	// Adding twice is a no-op, not an error or a duplicate row.
	if err := s.AddItemToCollection(ctx, col.ID, in.ID); err != nil {
		t.Fatalf("re-add item: %v", err)
	}

	items, total, err := s.ListItemSummaries(ctx, store.ListItemsOpts{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("filtered summaries: %v", err)
	}
	if total != 2 || len(items) != 2 || items[0].ID != in.ID || items[1].ID != second.ID {
		t.Fatalf("filtered total=%d items=%+v", total, items)
	}
	if len(items[0].CollectionIDs) != 1 || items[0].CollectionIDs[0] != col.ID {
		t.Fatalf("collection ids = %+v", items[0].CollectionIDs)
	}
	summaries, err := s.ListCollections(ctx)
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ItemCount != 2 || len(summaries[0].ThumbItemIDs) != 2 {
		t.Fatalf("collection summaries = %+v", summaries)
	}
	if err := s.ReorderCollection(ctx, col.ID, []int64{second.ID, in.ID}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	items, _, err = s.ListItemSummaries(ctx, store.ListItemsOpts{CollectionID: col.ID})
	if err != nil {
		t.Fatalf("filtered after reorder: %v", err)
	}
	if items[0].ID != second.ID || items[1].ID != in.ID {
		t.Fatalf("reordered items = %+v", items)
	}
	if err := s.ReorderCollection(ctx, col.ID, []int64{in.ID}); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("bad reorder err = %v", err)
	}
	renamed, err := s.UpdateCollection(ctx, col.ID, "Watchlist")
	if err != nil || renamed.Name != "Watchlist" {
		t.Fatalf("rename = %+v err=%v", renamed, err)
	}
	if err := s.RemoveItemFromCollection(ctx, col.ID, in.ID); err != nil {
		t.Fatalf("remove item: %v", err)
	}
	summaries, err = s.ListCollections(ctx)
	if err != nil {
		t.Fatalf("list collections after remove: %v", err)
	}
	if summaries[0].ItemCount != 1 {
		t.Fatalf("item count after remove = %+v", summaries[0])
	}

	_, total, err = s.ListItemSummaries(ctx, store.ListItemsOpts{})
	if err != nil || total != 3 {
		t.Fatalf("unfiltered total=%d err=%v", total, err)
	}
	_ = out
}

func TestUncollectedFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	alpha, _ := s.CreateItem(ctx, store.NewItem{Title: "Orbit Alpha"})
	beta, _ := s.CreateItem(ctx, store.NewItem{Title: "Orbit Beta"})
	_, _ = s.CreateItem(ctx, store.NewItem{Title: "Orbit Gamma"}) // never collected
	col, err := s.CreateCollection(ctx, "Shelf")
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if err := s.AddItemToCollection(ctx, col.ID, alpha.ID); err != nil {
		t.Fatalf("add alpha: %v", err)
	}

	// Uncollected returns everything except the item in a collection.
	items, total, err := s.ListItemSummaries(ctx, store.ListItemsOpts{Uncollected: true})
	if err != nil {
		t.Fatalf("uncollected list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("uncollected total=%d items=%+v, want 2", total, items)
	}
	for _, it := range items {
		if it.ID == alpha.ID {
			t.Fatalf("collected item leaked into uncollected list: %+v", items)
		}
	}

	// A specific collection_id wins when both are set (documented contract).
	_, total, err = s.ListItemSummaries(ctx, store.ListItemsOpts{CollectionID: col.ID, Uncollected: true})
	if err != nil || total != 1 {
		t.Fatalf("collection_id should override uncollected: total=%d err=%v", total, err)
	}

	// Collecting beta drops it out of the uncollected view, leaving only gamma.
	if err := s.AddItemToCollection(ctx, col.ID, beta.ID); err != nil {
		t.Fatalf("add beta: %v", err)
	}
	_, total, err = s.ListItemSummaries(ctx, store.ListItemsOpts{Uncollected: true})
	if err != nil || total != 1 {
		t.Fatalf("after collecting beta: uncollected total=%d err=%v", total, err)
	}

	// Search honours the same filter.
	found, total, err := s.SearchItemSummaries(ctx, store.SearchItemsOpts{Query: "orbit", Uncollected: true, Limit: 10})
	if err != nil {
		t.Fatalf("uncollected search: %v", err)
	}
	if total != 1 || len(found) != 1 || found[0].Title != "Orbit Gamma" {
		t.Fatalf("uncollected search total=%d items=%+v", total, found)
	}
}

func TestGetItemSummary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	year := 2008
	item, _ := s.CreateItem(ctx, store.NewItem{Title: "Big Buck Bunny", Year: &year})
	got, err := s.GetItemSummary(ctx, item.ID)
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	if got.ID != item.ID || got.Title != "Big Buck Bunny" || got.Year == nil || *got.Year != 2008 {
		t.Fatalf("summary = %+v", got)
	}
	if got.Available {
		t.Fatal("item with no files reported available")
	}
	if _, err := s.GetItemSummary(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing item err = %v", err)
	}
}
