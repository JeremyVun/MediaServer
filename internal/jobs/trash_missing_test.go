package jobs

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
)

type missingFixture struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	mgr   *Manager
	root  store.Root
	evs   <-chan events.Event
	logs  *bytes.Buffer
	items []store.Item
	paths map[int64]string // item id → rel path of its first file
}

// newMissingFixture builds a root holding n one-file movies, on disk and
// in the catalog, already probed so a reconcile walk queues nothing.
func newMissingFixture(t *testing.T, n int) *missingFixture {
	t.Helper()
	st := newTestStore(t)
	root, err := st.UpsertRoot(context.Background(), "Media", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	evs, cancel := bus.Subscribe()
	t.Cleanup(cancel)
	logs := &bytes.Buffer{}
	mgr := NewManager(Options{
		Store: st, Bus: bus, ThumbsDir: t.TempDir(),
		Log:     slog.New(slog.NewTextHandler(logs, nil)),
		FFprobe: testScript(t, "ffprobe", fakeFFprobe),
	})
	f := &missingFixture{t: t, ctx: context.Background(), st: st, mgr: mgr, root: root, evs: evs, logs: logs,
		paths: map[int64]string{}}
	for i := 0; i < n; i++ {
		f.items = append(f.items, f.add(fmt.Sprintf("movies/Movie %02d.mkv", i)))
	}
	return f
}

func (f *missingFixture) write(rel string) (os.FileInfo, string) {
	f.t.Helper()
	path := filepath.Join(f.root.Path, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bytes of "+rel), 0o644); err != nil {
		f.t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		f.t.Fatal(err)
	}
	fp, err := library.Fingerprint(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return info, fp
}

func (f *missingFixture) add(rel string) store.Item {
	f.t.Helper()
	info, fp := f.write(rel)
	item, file, err := f.st.CreateItemWithFile(f.ctx, store.NewItem{Type: "movie", Title: rel}, store.NewFile{
		RootID: f.root.ID, RelPath: rel, Size: info.Size(), Mtime: info.ModTime(), Fingerprint: fp,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.probed(file.ID)
	f.paths[item.ID] = rel
	return item
}

func (f *missingFixture) addFile(item store.Item, rel string) store.File {
	f.t.Helper()
	info, fp := f.write(rel)
	file, err := f.st.CreateFile(f.ctx, store.NewFile{
		ItemID: item.ID, RootID: f.root.ID, RelPath: rel, Size: info.Size(), Mtime: info.ModTime(), Fingerprint: fp,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.probed(file.ID)
	return file
}

func (f *missingFixture) probed(fileID int64) {
	f.t.Helper()
	if err := f.st.UpdateFileProbe(f.ctx, fileID, store.ProbeResult{Container: "matroska", DurationS: 3000}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *missingFixture) remove(rel string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.root.Path, rel)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *missingFixture) reconcile() {
	f.t.Helper()
	if err := f.mgr.handleReconcile(f.ctx, f.root.ID); err != nil {
		f.t.Fatalf("reconcile: %v", err)
	}
}

// sweepAt runs the sweep with its clock moved d into the future.
func (f *missingFixture) sweepAt(d time.Duration) {
	f.t.Helper()
	f.mgr.now = func() time.Time { return time.Now().Add(d) }
	if err := f.mgr.handleTrashMissing(f.ctx); err != nil {
		f.t.Fatalf("sweep: %v", err)
	}
}

func (f *missingFixture) trashed(item store.Item) bool {
	f.t.Helper()
	got, err := f.st.GetItem(f.ctx, item.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return got.DeletedAt != nil
}

func (f *missingFixture) file(item store.Item) store.File {
	f.t.Helper()
	files, err := f.st.ListFilesForItem(f.ctx, item.ID)
	if err != nil || len(files) == 0 {
		f.t.Fatalf("files of %d: %v %v", item.ID, files, err)
	}
	return files[0]
}

func (f *missingFixture) missingSince(fileID int64) *string {
	f.t.Helper()
	var v *string
	if err := f.st.DB().QueryRowContext(f.ctx, `SELECT missing_since FROM media_files WHERE id = ?`, fileID).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *missingFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.st.DB().ExecContext(f.ctx, query, args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

func (f *missingFixture) watching(item store.Item) {
	f.exec(`INSERT INTO watch_progress (item_id, position_s, duration_s) VALUES (?, 600, 3000)`, item.ID)
}

func (f *missingFixture) liveTrashedCount() (live, trashed int) {
	f.t.Helper()
	for _, it := range f.items {
		if f.trashed(it) {
			trashed++
		} else {
			live++
		}
	}
	return live, trashed
}

func (f *missingFixture) drainEvents() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-f.evs:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func (f *missingFixture) queuedSweeps() []store.Job {
	f.t.Helper()
	jobs, err := f.st.ListJobs(f.ctx, store.ListJobsOpts{Status: "queued", Limit: 200})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.Job
	for _, j := range jobs {
		if j.Type == TypeTrashMissing {
			out = append(out, j)
		}
	}
	return out
}

func skipIfRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
}

func TestTrashMissingAfterGrace(t *testing.T) {
	f := newMissingFixture(t, 10)
	gone := f.items[0]
	f.watching(gone)
	f.remove(f.paths[gone.ID])
	f.reconcile()

	file := f.file(gone)
	since := f.missingSince(file.ID)
	if file.Status != "missing" || since == nil {
		t.Fatalf("after the walk: status %q, missing_since %v", file.Status, since)
	}
	if len(f.queuedSweeps()) != 1 {
		t.Fatalf("the walk queued sweeps %+v, want one", f.queuedSweeps())
	}

	job, err := f.st.ClaimNextJob(f.ctx)
	if err != nil || job.Type != TypeTrashMissing {
		t.Fatalf("claimed %+v %v, want the queued sweep", job, err)
	}
	f.mgr.now = func() time.Time { return time.Now().Add(5 * time.Minute) }
	f.mgr.runJob(f.ctx, job)
	if f.trashed(gone) {
		t.Fatal("trashed inside the grace")
	}
	sweeps := f.queuedSweeps()
	missingAt, _ := store.ParseTime(*since)
	if want := store.FormatTime(missingAt.Add(missingGrace + time.Second)); len(sweeps) != 1 || sweeps[0].RunAt != want {
		t.Fatalf("follow-up sweeps %+v, want one at %s", sweeps, want)
	}
	f.drainEvents()

	f.sweepAt(11 * time.Minute)
	if !f.trashed(gone) {
		t.Fatal("not trashed after the grace")
	}
	if live, trashed := f.liveTrashedCount(); live != 9 || trashed != 1 {
		t.Fatalf("live %d trashed %d, want 9 and 1", live, trashed)
	}
	if got := f.file(gone); got.Status != "missing" {
		t.Fatalf("file status %q, want it left missing", got.Status)
	}
	removed := 0
	for _, ev := range f.drainEvents() {
		if ev.Type == events.ItemRemoved && ev.Payload.(map[string]any)["id"] == gone.ID {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("item.removed published %d times, want once", removed)
	}

	live, _, err := f.st.ListItemSummaries(f.ctx, store.ListItemsOpts{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range live {
		if it.ID == gone.ID {
			t.Fatal("trashed item still in the library")
		}
	}
	watching, _, err := f.st.ListItemSummaries(f.ctx, store.ListItemsOpts{InProgress: true, Sort: "watched"})
	if err != nil {
		t.Fatal(err)
	}
	if len(watching) != 0 {
		t.Fatalf("continue watching still holds %+v", watching)
	}
	trash, _, err := f.st.ListItemSummaries(f.ctx, store.ListItemsOpts{Trashed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 || trash[0].ID != gone.ID {
		t.Fatalf("trash = %+v", trash)
	}
	if p, err := f.st.GetProgress(f.ctx, gone.ID); err != nil || p == nil {
		t.Fatalf("progress lost on trash: %v %v", p, err)
	}
}

func TestTrashMissingKeepsAFileMovedWithinGrace(t *testing.T) {
	f := newMissingFixture(t, 10)
	moved := f.items[0]
	f.watching(moved)
	before := f.file(moved)
	away := filepath.Join(t.TempDir(), "elsewhere.mkv")
	if err := os.Rename(filepath.Join(f.root.Path, before.RelPath), away); err != nil {
		t.Fatal(err)
	}
	f.reconcile()
	if got := f.file(moved); got.Status != "missing" {
		t.Fatalf("status %q while away, want missing", got.Status)
	}

	back := filepath.Join(f.root.Path, "renamed", "Movie 00 (2026).mkv")
	if err := os.MkdirAll(filepath.Dir(back), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, back); err != nil {
		t.Fatal(err)
	}
	f.reconcile()
	f.sweepAt(11 * time.Minute)

	if f.trashed(moved) {
		t.Fatal("a file moved back within the grace lost its item")
	}
	after := f.file(moved)
	if after.ID != before.ID || after.RelPath != "renamed/Movie 00 (2026).mkv" || after.Status != "online" {
		t.Fatalf("file after the move = %+v, want id %d online at the new path", after, before.ID)
	}
	if since := f.missingSince(after.ID); since != nil {
		t.Fatalf("missing_since %q kept after re-pairing", *since)
	}
	if p, err := f.st.GetProgress(f.ctx, moved.ID); err != nil || p == nil || p.PositionS != 600 {
		t.Fatalf("progress after the move = %+v %v", p, err)
	}
}

func TestTrashMissingNeverTrashesFromOfflineRoot(t *testing.T) {
	t.Run("root marked offline", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		f.remove(f.paths[f.items[0].ID])
		f.reconcile()
		if err := f.st.SetRootOnline(f.ctx, f.root.ID, false); err != nil {
			t.Fatal(err)
		}
		f.sweepAt(11 * time.Minute)
		if f.trashed(f.items[0]) {
			t.Fatal("trashed from an offline root")
		}
	})
	t.Run("root path unmounted", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		f.remove(f.paths[f.items[0].ID])
		f.reconcile()
		if err := os.Rename(f.root.Path, f.root.Path+"-unmounted"); err != nil {
			t.Fatal(err)
		}
		f.sweepAt(11 * time.Minute)
		if _, trashed := f.liveTrashedCount(); trashed != 0 {
			t.Fatalf("trashed %d items from an unmounted root", trashed)
		}
	})
}

func TestTrashMissingNeverTrashesFromUnreadableRoot(t *testing.T) {
	skipIfRoot(t)
	t.Run("root cannot be listed", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		f.remove(f.paths[f.items[0].ID])
		f.reconcile()
		// Write and search but no read: names still resolve, so only the
		// listing check stands between this and a trash.
		if err := os.Chmod(f.root.Path, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(f.root.Path, 0o755) })
		f.sweepAt(11 * time.Minute)
		if f.trashed(f.items[0]) {
			t.Fatal("trashed from a root that cannot be listed")
		}
		if !strings.Contains(f.logs.String(), "library root unreadable") {
			t.Fatalf("no unreadable warning in %q", f.logs.String())
		}
	})
	t.Run("file cannot be checked", func(t *testing.T) {
		f := newMissingFixture(t, 9)
		hidden := f.add("locked/Movie.mkv")
		f.items = append(f.items, hidden)
		f.remove(f.paths[hidden.ID])
		f.reconcile()
		locked := filepath.Join(f.root.Path, "locked")
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		f.sweepAt(11 * time.Minute)
		if f.trashed(hidden) {
			t.Fatal("trashed a file whose stat failed with a permission error")
		}
		if !strings.Contains(f.logs.String(), "cannot check missing file") {
			t.Fatalf("no stat warning in %q", f.logs.String())
		}
	})
}

func TestTrashMissingMassVanishGuard(t *testing.T) {
	cases := []struct {
		files, missing int
		blocked        bool
	}{
		{files: 4, missing: 1},                  // 25 %, but too few to suspect the volume
		{files: 40, missing: 10},                // 25 %, still only 10 files
		{files: 40, missing: 11, blocked: true}, // over both limits
		{files: 60, missing: 12},                // 12 files, but exactly 20 %
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d of %d", c.missing, c.files), func(t *testing.T) {
			f := newMissingFixture(t, c.files)
			for _, it := range f.items[:c.missing] {
				f.remove(f.paths[it.ID])
			}
			f.reconcile()
			f.sweepAt(11 * time.Minute)
			want := c.missing
			if c.blocked {
				want = 0
			}
			if _, trashed := f.liveTrashedCount(); trashed != want {
				t.Fatalf("trashed %d, want %d", trashed, want)
			}
			warned := strings.Contains(f.logs.String(), "too many missing")
			if warned != c.blocked {
				t.Fatalf("warning logged = %v, want %v: %s", warned, c.blocked, f.logs.String())
			}
			if c.blocked {
				for _, want := range []string{"level=WARN", "name=Media", fmt.Sprintf("missing=%d", c.missing), fmt.Sprintf("files=%d", c.files)} {
					if !strings.Contains(f.logs.String(), want) {
						t.Fatalf("warning lacks %q: %s", want, f.logs.String())
					}
				}
			}
		})
	}
}

func TestTrashMissingKeepsPartlyMissingItem(t *testing.T) {
	f := newMissingFixture(t, 10)
	two := f.items[0]
	second := f.addFile(two, "movies/Movie 00 part 2.mkv")
	f.remove(f.paths[two.ID])
	f.reconcile()
	f.sweepAt(11 * time.Minute)
	if f.trashed(two) {
		t.Fatal("trashed an item with one of its two files still on disk")
	}

	f.remove(second.RelPath)
	f.reconcile()
	f.sweepAt(11 * time.Minute)
	if !f.trashed(two) {
		t.Fatal("not trashed once both files were gone past the grace")
	}
}

func TestRestoredAutoTrashedItemGetsAFreshGrace(t *testing.T) {
	f := newMissingFixture(t, 10)
	gone := f.items[0]
	f.remove(f.paths[gone.ID])
	f.reconcile()
	f.exec(`UPDATE media_files SET missing_since = datetime('now', '-1 hour') WHERE item_id = ?`, gone.ID)
	f.sweepAt(0)
	if !f.trashed(gone) {
		t.Fatal("not trashed an hour after its file went missing")
	}

	if err := f.st.MarkItemRestored(f.ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	file := f.file(gone)
	since := f.missingSince(file.ID)
	if file.Status != "missing" || since == nil {
		t.Fatalf("restored file: status %q missing_since %v", file.Status, since)
	}
	if at, _ := store.ParseTime(*since); time.Since(at) > time.Minute {
		t.Fatalf("restore kept the old missing_since %s", *since)
	}

	f.sweepAt(5 * time.Minute)
	if f.trashed(gone) {
		t.Fatal("re-trashed inside the fresh grace")
	}
	f.sweepAt(11 * time.Minute)
	if !f.trashed(gone) {
		t.Fatal("not re-trashed once the fresh grace ran out")
	}
}

func TestCleanupPurgesAutoTrashedItemWithoutAFile(t *testing.T) {
	f := newMissingFixture(t, 10)
	gone := f.items[0]
	f.remove(f.paths[gone.ID])
	f.reconcile()
	f.sweepAt(11 * time.Minute)
	if !f.trashed(gone) {
		t.Fatal("setup: not trashed")
	}
	f.drainEvents()

	f.mgr.trashRetentionDays = 0
	if err := f.mgr.handleCleanup(f.ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := f.st.GetItem(f.ctx, gone.ID); err != store.ErrNotFound {
		t.Fatalf("item after purge: %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(f.root.Path, ".trash")); !os.IsNotExist(err) {
		t.Fatalf(".trash touched by the purge: %v", err)
	}
	for _, it := range f.items[1:] {
		if _, err := os.Stat(filepath.Join(f.root.Path, f.paths[it.ID])); err != nil {
			t.Fatalf("purge disturbed %s: %v", f.paths[it.ID], err)
		}
	}
	removed := false
	for _, ev := range f.drainEvents() {
		if ev.Type == events.ItemRemoved && ev.Payload.(map[string]any)["id"] == gone.ID {
			removed = true
		}
	}
	if !removed {
		t.Fatal("no item.removed for the purge")
	}
}

func TestTrashMissingTakesAShowOffTheGrid(t *testing.T) {
	f := newMissingFixture(t, 8)
	show, err := f.st.CreateShow(f.ctx, store.NewShow{Title: "MobLand"})
	if err != nil {
		t.Fatal(err)
	}
	episode := "episode"
	var episodes []store.Item
	for n := 1; n <= 2; n++ {
		ep := f.add(fmt.Sprintf("incoming/MobLand S01E%02d.mkv", n))
		if _, err := f.st.UpdateItem(f.ctx, ep.ID, store.UpdateItemParams{
			Type: &episode, ShowID: store.SetTo(show.ID), Season: store.SetTo(1), Episode: store.SetTo(n),
			ClassifiedAt: store.SetTo(store.FormatTime(time.Now())),
		}); err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, ep)
	}
	onGrid := func() bool {
		entries, _, err := f.st.ListEntries(f.ctx, store.ListEntriesOpts{Type: "show"})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Show != nil && e.Show.ID == show.ID {
				return true
			}
		}
		return false
	}
	if !onGrid() {
		t.Fatal("setup: show not on the grid")
	}

	for _, ep := range episodes {
		f.remove(f.paths[ep.ID])
	}
	f.reconcile()
	f.drainEvents()
	f.sweepAt(11 * time.Minute)

	for _, ep := range episodes {
		if !f.trashed(ep) {
			t.Fatalf("episode %d not trashed", ep.ID)
		}
	}
	if onGrid() {
		t.Fatal("show still on the grid with every episode trashed")
	}
	removed, showUpdates := 0, 0
	for _, ev := range f.drainEvents() {
		switch ev.Type {
		case events.ItemRemoved:
			removed++
		case events.ShowUpdated:
			if ev.Payload.(map[string]any)["id"] == show.ID {
				showUpdates++
			}
		}
	}
	if removed != 2 || showUpdates != 1 {
		t.Fatalf("item.removed %d, show.updated %d; want 2 and 1", removed, showUpdates)
	}
}

// autoTrash takes item's file off the root and lets the sweep trash it.
func (f *missingFixture) autoTrash(item store.Item) (away string) {
	f.t.Helper()
	away = filepath.Join(f.t.TempDir(), "away.mkv")
	if err := os.Rename(filepath.Join(f.root.Path, f.paths[item.ID]), away); err != nil {
		f.t.Fatal(err)
	}
	f.reconcile()
	f.sweepAt(11 * time.Minute)
	if !f.trashed(item) {
		f.t.Fatal("setup: not auto-trashed")
	}
	f.drainEvents()
	return away
}

func (f *missingFixture) putBack(away, rel string) {
	f.t.Helper()
	path := filepath.Join(f.root.Path, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(away, path); err != nil {
		f.t.Fatal(err)
	}
}

func (f *missingFixture) assertRestored(item store.Item, rel string, fileID int64) {
	f.t.Helper()
	if f.trashed(item) {
		f.t.Fatal("still in Trash after its file came back")
	}
	got := f.file(item)
	if got.ID != fileID || got.RelPath != rel || got.Status != "online" {
		f.t.Fatalf("file = %+v, want id %d online at %s", got, fileID, rel)
	}
	if p, err := f.st.GetProgress(f.ctx, item.ID); err != nil || p == nil || p.PositionS != 600 {
		f.t.Fatalf("progress after restore = %+v %v", p, err)
	}
	added := false
	for _, ev := range f.drainEvents() {
		if s, ok := ev.Payload.(store.ItemSummary); ok && ev.Type == events.ItemAdded && s.ID == item.ID {
			added = true
		}
	}
	if !added {
		f.t.Fatal("no item.added for the restore")
	}
	if !strings.Contains(f.logs.String(), "restored from trash: file is back") {
		f.t.Fatalf("restore not logged: %s", f.logs.String())
	}
}

func TestAutoTrashedItemRestoresWhenItsFileReturns(t *testing.T) {
	t.Run("same path, reconcile", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		item := f.items[0]
		f.watching(item)
		before := f.file(item)
		f.putBack(f.autoTrash(item), before.RelPath)
		f.reconcile()
		f.assertRestored(item, before.RelPath, before.ID)
	})
	t.Run("same path, probe", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		item := f.items[0]
		f.watching(item)
		before := f.file(item)
		f.putBack(f.autoTrash(item), before.RelPath)
		if err := f.mgr.handleProbe(f.ctx, f.root.ID, before.RelPath); err != nil {
			t.Fatal(err)
		}
		f.assertRestored(item, before.RelPath, before.ID)
	})
	t.Run("renamed, fingerprint", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		item := f.items[0]
		f.watching(item)
		before := f.file(item)
		f.putBack(f.autoTrash(item), "back/Movie 00 (2026).mkv")
		f.reconcile()
		f.assertRestored(item, "back/Movie 00 (2026).mkv", before.ID)
	})
	t.Run("episode refreshes its show", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		show, err := f.st.CreateShow(f.ctx, store.NewShow{Title: "MobLand"})
		if err != nil {
			t.Fatal(err)
		}
		episode := "episode"
		ep := f.add("incoming/MobLand S01E01.mkv")
		if _, err := f.st.UpdateItem(f.ctx, ep.ID, store.UpdateItemParams{
			Type: &episode, ShowID: store.SetTo(show.ID), Season: store.SetTo(1), Episode: store.SetTo(1),
			ClassifiedAt: store.SetTo(store.FormatTime(time.Now())),
		}); err != nil {
			t.Fatal(err)
		}
		f.putBack(f.autoTrash(ep), "incoming/MobLand S01E01.mkv")
		f.reconcile()
		if f.trashed(ep) {
			t.Fatal("episode still in Trash")
		}
		updated := false
		for _, ev := range f.drainEvents() {
			if m, ok := ev.Payload.(map[string]any); ok && ev.Type == events.ShowUpdated && m["id"] == show.ID {
				updated = true
			}
		}
		if !updated {
			t.Fatal("no show.updated for the restored episode")
		}
	})
}

func TestOwnerTrashedItemNeverAutoRestores(t *testing.T) {
	// ownerTrash moves item's file into .trash the way DELETE /api/items does.
	ownerTrash := func(f *missingFixture, item store.Item) (file store.File, live, trashCopy string) {
		t.Helper()
		file = f.file(item)
		live = filepath.Join(f.root.Path, file.RelPath)
		trashCopy = filepath.Join(f.root.Path, ".trash", fmt.Sprintf("%d_%s", file.ID, filepath.Base(live)))
		if err := os.MkdirAll(filepath.Dir(trashCopy), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(live, trashCopy); err != nil {
			t.Fatal(err)
		}
		if err := f.st.MarkItemTrashed(f.ctx, item.ID); err != nil {
			t.Fatal(err)
		}
		return file, live, trashCopy
	}
	copyTo := func(t *testing.T, from string, to ...string) {
		body, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range to {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("same bytes back at the old path and renamed", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		item := f.items[0]
		file, live, trashCopy := ownerTrash(f, item)
		copyTo(t, trashCopy, live, filepath.Join(f.root.Path, "copy/again.mkv"))
		f.reconcile()
		if err := f.mgr.handleProbe(f.ctx, f.root.ID, file.RelPath); err != nil {
			t.Fatal(err)
		}
		if !f.trashed(item) {
			t.Fatal("an item the owner trashed restored itself")
		}
		got := f.file(item)
		if got.Status != "trashed" || got.RelPath != file.RelPath {
			t.Fatalf("owner-trashed file = %+v, want still trashed at its old path", got)
		}
		if _, err := os.Stat(trashCopy); err != nil {
			t.Fatalf("trash copy disturbed: %v", err)
		}
	})
	t.Run("status lost to an unmount before B16", func(t *testing.T) {
		// Unmounts used to overwrite 'trashed', and a remount then marked the
		// file missing; such a row re-pairs by fingerprint like any other.
		f := newMissingFixture(t, 10)
		item := f.items[0]
		file, _, trashCopy := ownerTrash(f, item)
		f.exec(`UPDATE media_files SET status = 'missing' WHERE id = ?`, file.ID)
		copyTo(t, trashCopy, filepath.Join(f.root.Path, "copy/again.mkv"))
		f.reconcile()
		if got := f.file(item); got.RelPath != "copy/again.mkv" || got.Status != "online" {
			t.Fatalf("setup: file = %+v, want re-paired online", got)
		}
		if !f.trashed(item) {
			t.Fatal("an item the owner trashed restored itself")
		}
	})
}
