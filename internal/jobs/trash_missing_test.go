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
		Log: slog.New(slog.NewTextHandler(logs, nil)),
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
	t.Run("over 20 percent", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		for _, it := range f.items[:3] {
			f.remove(f.paths[it.ID])
		}
		f.reconcile()
		f.sweepAt(11 * time.Minute)
		if _, trashed := f.liveTrashedCount(); trashed != 0 {
			t.Fatalf("trashed %d with 3 of 10 files missing", trashed)
		}
		logs := f.logs.String()
		for _, want := range []string{"level=WARN", "too many missing", "name=Media", "missing=3", "files=10"} {
			if !strings.Contains(logs, want) {
				t.Fatalf("warning lacks %q: %s", want, logs)
			}
		}
	})
	t.Run("exactly 20 percent", func(t *testing.T) {
		f := newMissingFixture(t, 10)
		for _, it := range f.items[:2] {
			f.remove(f.paths[it.ID])
		}
		f.reconcile()
		f.sweepAt(11 * time.Minute)
		if _, trashed := f.liveTrashedCount(); trashed != 2 {
			t.Fatalf("trashed %d with 2 of 10 files missing, want 2", trashed)
		}
	})
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
