package jobs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
)

type fakePruner struct {
	calls int
}

func (p *fakePruner) PruneCache(context.Context) error {
	p.calls++
	return nil
}

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

func TestReconcileProbeThumbnailPipeline(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rootPath := t.TempDir()
	mediaPath := filepath.Join(rootPath, "Big.Buck.Bunny.(2008).mp4")
	if err := os.WriteFile(mediaPath, []byte("not real media; fake ffprobe handles this"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	upload, err := st.CreateUpload(ctx, store.NewUpload{
		ID:       "upload-1",
		Filename: "Big.Buck.Bunny.(2008).mp4",
		Size:     1,
		RootID:   root.ID,
	})
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	if _, err := st.MarkUploadComplete(ctx, upload.ID, "Big.Buck.Bunny.(2008).mp4"); err != nil {
		t.Fatalf("complete upload: %v", err)
	}

	ffprobe := testScript(t, "ffprobe", `#!/bin/sh
cat <<'JSON'
{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":640,"height":360},{"index":1,"codec_name":"aac","codec_type":"audio","channels":2,"disposition":{"default":1},"tags":{"language":"eng"}}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"12.5","bit_rate":"1000"}}
JSON
`)
	ffmpeg := testScript(t, "ffmpeg", `#!/bin/sh
eval "out=\${$#}"
printf 'jpeg' > "$out"
`)
	thumbsDir := filepath.Join(t.TempDir(), "thumbs")
	bus := events.NewBus()
	defer bus.Close()
	eventCh, cancel := bus.Subscribe()
	defer cancel()
	mgr := NewManager(Options{Store: st, Bus: bus, FFprobe: ffprobe, FFmpeg: ffmpeg, ThumbsDir: thumbsDir})

	if err := mgr.handleReconcile(ctx, root.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	probeJob, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim probe: %v", err)
	}
	if probeJob.Type != TypeProbe {
		t.Fatalf("job type = %q", probeJob.Type)
	}
	mgr.runJob(ctx, probeJob)

	// item.added must carry the full hydrated summary (SPEC-API SSE
	// contract), never a bare id.
	select {
	case ev := <-eventCh:
		if ev.Type != events.ItemAdded {
			t.Fatalf("event type = %q", ev.Type)
		}
		summary, ok := ev.Payload.(store.ItemSummary)
		if !ok {
			t.Fatalf("payload = %T, want store.ItemSummary", ev.Payload)
		}
		if summary.Title != "Big Buck Bunny" || summary.DurationS == nil {
			t.Fatalf("summary payload = %+v", summary)
		}
	case <-time.After(time.Second):
		t.Fatal("no item.added event published")
	}
	select {
	case ev := <-eventCh:
		if ev.Type != events.UploadComplete {
			t.Fatalf("event type = %q, want upload.complete", ev.Type)
		}
		payload, ok := ev.Payload.(map[string]any)
		if !ok || payload["id"] != upload.ID {
			t.Fatalf("upload complete payload = %#v", ev.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no upload.complete event published")
	}

	thumbJob, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim thumbnail: %v", err)
	}
	if thumbJob.Type != TypeThumbnail {
		t.Fatalf("job type = %q", thumbJob.Type)
	}
	mgr.runJob(ctx, thumbJob)

	items, total, err := st.ListItemSummaries(ctx, store.ListItemsOpts{})
	if err != nil {
		t.Fatalf("list summaries: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d items=%+v", total, items)
	}
	if items[0].Title != "Big Buck Bunny" || items[0].Year == nil || *items[0].Year != 2008 {
		t.Fatalf("parsed item = %+v", items[0])
	}
	if !items[0].Available || items[0].DurationS == nil || *items[0].DurationS != 12.5 {
		t.Fatalf("summary metadata = %+v", items[0])
	}

	files, err := st.ListFilesForItem(ctx, items[0].ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	streams, err := st.ListFileStreams(ctx, files[0].ID)
	if err != nil {
		t.Fatalf("streams: %v", err)
	}
	if len(streams) != 2 || streams[0].Codec != "h264" || streams[1].Codec != "aac" {
		t.Fatalf("streams = %+v", streams)
	}
	if _, err := os.Stat(filepath.Join(thumbsDir, "1.jpg")); err != nil {
		t.Fatalf("thumbnail missing: %v", err)
	}
}

func TestCorruptProbeMarksJobFailed(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "bad.mp4"), []byte("bad"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	root, _ := st.UpsertRoot(ctx, "A", rootPath)
	ffprobe := testScript(t, "ffprobe", `#!/bin/sh
echo 'Invalid data found when processing input' >&2
exit 1
`)
	mgr := NewManager(Options{Store: st, FFprobe: ffprobe, FFmpeg: ffprobe, ThumbsDir: t.TempDir()})
	job, err := mgr.enqueueProbe(ctx, root.ID, "bad.mp4")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	mgr.runJob(ctx, claimed)

	got, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != "failed" || got.Attempts != 1 || got.Error == nil {
		t.Fatalf("failed job = %+v", got)
	}
	if !strings.Contains(*got.Error, "Invalid data") {
		t.Fatalf("error = %q", *got.Error)
	}
}

func TestCleanupPurgesTrashUploadsAndHLS(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movie.mp4",
		Size:        5,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if err := st.MarkItemTrashed(ctx, item.ID); err != nil {
		t.Fatalf("trash item: %v", err)
	}
	trashPath := filepath.Join(rootPath, ".trash", strconv.FormatInt(file.ID, 10)+"_movie.mp4")
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatalf("mkdir trash: %v", err)
	}
	if err := os.WriteFile(trashPath, []byte("movie"), 0o644); err != nil {
		t.Fatalf("write trash: %v", err)
	}

	partPath := filepath.Join(rootPath, "incoming", ".uploads", "old-upload.part")
	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		t.Fatalf("mkdir upload part: %v", err)
	}
	if err := os.WriteFile(partPath, []byte("part"), 0o644); err != nil {
		t.Fatalf("write upload part: %v", err)
	}
	if _, err := st.CreateUpload(ctx, store.NewUpload{
		ID:       "old-upload",
		Filename: "old.mp4",
		Size:     4,
		RootID:   root.ID,
	}); err != nil {
		t.Fatalf("create upload: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `
		UPDATE uploads SET updated_at = datetime('now', '-8 days') WHERE id = 'old-upload'`); err != nil {
		t.Fatalf("age upload: %v", err)
	}

	pruner := &fakePruner{}
	mgr := NewManager(Options{
		Store:              st,
		ThumbsDir:          t.TempDir(),
		TrashRetentionDays: 0,
		UploadMaxAge:       7 * 24 * time.Hour,
		HLSPruner:          pruner,
	})
	if err := mgr.handleCleanup(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(trashPath); !os.IsNotExist(err) {
		t.Fatalf("trash path err=%v, want not exist", err)
	}
	if _, err := st.GetItem(ctx, item.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("item err=%v, want ErrNotFound", err)
	}
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Fatalf("part path err=%v, want not exist", err)
	}
	if _, err := st.GetUpload(ctx, "old-upload"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("upload err=%v, want ErrNotFound", err)
	}
	if pruner.calls != 1 {
		t.Fatalf("hls pruner calls = %d, want 1", pruner.calls)
	}
}

func TestReconcileRemountFlipsStatusWithoutReprobe(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rootPath := t.TempDir()
	mediaPath := filepath.Join(rootPath, "movie.mp4")
	if err := os.WriteFile(mediaPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	root, _ := st.UpsertRoot(ctx, "A", rootPath)
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID: item.ID, RootID: root.ID, RelPath: "movie.mp4",
		Size: info.Size(), Mtime: info.ModTime(), Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if err := st.UpdateFileProbe(ctx, file.ID, store.ProbeResult{Container: "mov", DurationS: 1}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	// Simulate the volume having been unplugged and remounted.
	if _, err := st.SetRootFilesStatus(ctx, root.ID, "offline"); err != nil {
		t.Fatalf("offline: %v", err)
	}

	mgr := NewManager(Options{Store: st, ThumbsDir: t.TempDir()})
	if err := mgr.handleReconcile(ctx, root.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, err := st.GetFile(ctx, file.ID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if got.Status != "online" {
		t.Fatalf("status = %q, want online", got.Status)
	}
	if _, err := st.ClaimNextJob(ctx); err == nil {
		t.Fatal("unchanged probed file was re-enqueued for probe on remount")
	}
}

func TestReconcileDetectsMovedFileByFingerprint(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rootPath := t.TempDir()
	oldPath := filepath.Join(rootPath, "old.mp4")
	newPath := filepath.Join(rootPath, "nested", "new.mp4")
	body := []byte("same media bytes")
	if err := os.WriteFile(oldPath, body, 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	fp, err := library.Fingerprint(oldPath)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	info, err := os.Stat(oldPath)
	if err != nil {
		t.Fatalf("stat old: %v", err)
	}
	root, _ := st.UpsertRoot(ctx, "A", rootPath)
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID: item.ID, RootID: root.ID, RelPath: "old.mp4",
		Size: info.Size(), Mtime: info.ModTime(), Fingerprint: fp,
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if err := st.UpdateFileProbe(ctx, file.ID, store.ProbeResult{Container: "mov", DurationS: 1}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	mgr := NewManager(Options{Store: st, ThumbsDir: t.TempDir()})
	if err := mgr.handleReconcile(ctx, root.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	moved, err := st.GetFileByLocation(ctx, root.ID, "nested/new.mp4")
	if err != nil {
		t.Fatalf("get moved file: %v", err)
	}
	if moved.ID != file.ID || moved.ItemID != item.ID {
		t.Fatalf("moved file = %+v, want id %d item %d", moved, file.ID, item.ID)
	}
	if _, err := st.GetFileByLocation(ctx, root.ID, "old.mp4"); err != store.ErrNotFound {
		t.Fatalf("old location err = %v, want ErrNotFound", err)
	}
	if _, err := st.ClaimNextJob(ctx); err == nil {
		t.Fatal("moved file was re-enqueued for probe")
	}
}

func TestRealFFmpegProbeThumbnailPipeline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	ctx := context.Background()
	st := newTestStore(t)
	rootPath := t.TempDir()
	mediaPath := filepath.Join(rootPath, "Generated.Sample.(2026).mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-f", "lavfi",
		"-i", "testsrc=size=160x90:rate=10",
		"-f", "lavfi",
		"-i", "anullsrc=channel_layout=stereo:sample_rate=44100",
		"-t", "1",
		"-shortest",
		"-c:v", "mpeg4",
		"-c:a", "aac",
		mediaPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v\n%s", err, out)
	}

	root, _ := st.UpsertRoot(ctx, "A", rootPath)
	thumbsDir := filepath.Join(t.TempDir(), "thumbs")
	mgr := NewManager(Options{Store: st, ThumbsDir: thumbsDir})
	if err := mgr.handleReconcile(ctx, root.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	probeJob, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim probe: %v", err)
	}
	mgr.runJob(ctx, probeJob)
	thumbJob, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim thumb: %v", err)
	}
	mgr.runJob(ctx, thumbJob)

	items, _, err := st.SearchItemSummaries(ctx, store.SearchItemsOpts{Query: "gen", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(items) != 1 || items[0].DurationS == nil || *items[0].DurationS <= 0 {
		t.Fatalf("items = %+v", items)
	}
	files, err := st.ListFilesForItem(ctx, items[0].ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	streams, err := st.ListFileStreams(ctx, files[0].ID)
	if err != nil {
		t.Fatalf("streams: %v", err)
	}
	if len(streams) < 2 {
		t.Fatalf("streams = %+v", streams)
	}
	if _, err := os.Stat(filepath.Join(thumbsDir, strconv.FormatInt(files[0].ID, 10)+".jpg")); err != nil {
		t.Fatalf("thumbnail missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(thumbsDir, strconv.FormatInt(files[0].ID, 10)+"_poster.jpg")); err != nil {
		t.Fatalf("poster missing: %v", err)
	}
}

func testScript(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}
