//go:build darwin

package watcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFSEventsFileCreateEnqueuesProbe(t *testing.T) {
	st, closeDB := newWatcherStore(t)
	defer closeDB()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rootPath, err := os.MkdirTemp("/private/tmp", "media-server-fsevents-*")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootPath) })

	preflightCtx, stopPreflight := context.WithCancel(ctx)
	if _, err := watchPath(preflightCtx, rootPath, 20*time.Millisecond, slog.Default()); err != nil {
		stopPreflight()
		t.Skipf("FSEvents unavailable in this process: %v", err)
	}
	stopPreflight()

	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	jobs := newFakeJobs()
	manager := NewManager(Options{
		Store:            st,
		Jobs:             jobs,
		Log:              slog.Default(),
		StreamLatency:    20 * time.Millisecond,
		Debounce:         5 * time.Millisecond,
		QuiesceInterval:  5 * time.Millisecond,
		QuiesceStableFor: 15 * time.Millisecond,
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	jobs.waitReconcile(t, root.ID)

	// Give the FSEvents stream a moment to enter its run loop before
	// creating the file that should be detected.
	time.Sleep(100 * time.Millisecond)

	mediaPath := filepath.Join(rootPath, "finder-copy.mp4")
	if err := os.WriteFile(mediaPath, []byte("media bytes"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}

	probe := jobs.waitProbe(t)
	if probe.rootID != root.ID || probe.relPath != "finder-copy.mp4" {
		t.Fatalf("probe = %+v", probe)
	}
}
