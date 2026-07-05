//go:build darwin

package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/jobs"
	"github.com/JeremyVun/MediaServer/internal/store"
)

// TestEventStormBulkCopy200 is the M9 event-storm acceptance: dropping 200
// files into a watched root at once must ingest every one, whether FSEvents
// delivers per-file events or coalesces the burst into a rescan. It wires the
// real watcher (real FSEvents) to the real job manager with fast stub
// ffprobe/ffmpeg so 200 probes stay cheap, then polls until all 200 land.
func TestEventStormBulkCopy200(t *testing.T) {
	st, closeDB := newWatcherStore(t)
	defer closeDB()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rootPath, err := os.MkdirTemp("/private/tmp", "media-server-storm-*")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootPath) })

	// Skip when FSEvents can't start (e.g. a sandboxed CI runner).
	preCtx, stopPre := context.WithCancel(ctx)
	if _, err := watchPath(preCtx, rootPath, 20*time.Millisecond, slog.Default()); err != nil {
		stopPre()
		t.Skipf("FSEvents unavailable in this process: %v", err)
	}
	stopPre()

	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	ffprobe := stormScript(t, "ffprobe", `#!/bin/sh
cat <<'JSON'
{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":640,"height":360},{"index":1,"codec_name":"aac","codec_type":"audio","channels":2,"disposition":{"default":1},"tags":{"language":"eng"}}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"12.5","bit_rate":"1000"}}
JSON
`)
	// Stub ffmpeg for thumbnails: touch the output (last arg) so the job succeeds.
	ffmpeg := stormScript(t, "ffmpeg", `#!/bin/sh
for a in "$@"; do out="$a"; done
: > "$out" 2>/dev/null || true
exit 0
`)

	jm := jobs.NewManager(jobs.Options{
		Store:           st,
		Bus:             events.NewBus(),
		Log:             slog.Default(),
		FFprobe:         ffprobe,
		FFmpeg:          ffmpeg,
		ThumbsDir:       t.TempDir(),
		Workers:         4,
		CleanupInterval: 0,
	})
	jm.Start(ctx)

	manager := NewManager(Options{
		Store:            st,
		Jobs:             jm,
		Log:              slog.Default(),
		StreamLatency:    20 * time.Millisecond,
		Debounce:         10 * time.Millisecond,
		QuiesceInterval:  10 * time.Millisecond,
		QuiesceStableFor: 30 * time.Millisecond,
		StormBackstop:    300 * time.Millisecond,
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	// Let the startup reconcile (empty dir) and the FSEvents run loop settle.
	time.Sleep(150 * time.Millisecond)

	const n = 200
	for i := range n {
		p := filepath.Join(rootPath, fmt.Sprintf("Storm.Movie.%03d.(2020).mp4", i))
		if err := os.WriteFile(p, fmt.Appendf(nil, "media bytes %d", i), 0o644); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(30 * time.Second)
	var have int
	for time.Now().Before(deadline) {
		files, err := st.ListFilesForRoot(ctx, root.ID)
		if err != nil {
			t.Fatalf("list files: %v", err)
		}
		if have = len(files); have >= n {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if have != n {
		for _, status := range []string{"queued", "running", "done", "failed"} {
			jobs, _ := st.ListJobs(ctx, store.ListJobsOpts{Status: status, Limit: 200})
			var sample string
			for _, j := range jobs {
				if j.Error != nil {
					sample = j.Type + ": " + *j.Error
					break
				}
			}
			t.Logf("jobs status=%s count=%d sampleErr=%q", status, len(jobs), sample)
		}
		t.Fatalf("ingested %d/%d files after storm", have, n)
	}
}

// stormScript writes an executable stub named like ffprobe/ffmpeg and returns
// its path, mirroring the jobs package's test helper without importing it.
func stormScript(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
	return path
}
