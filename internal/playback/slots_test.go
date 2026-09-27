package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sleepFFmpeg stands in for a long transcode: it ignores its arguments and
// never writes a segment, so only the worker's own bookkeeping ends it.
func sleepFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func startTranscodeSession(t *testing.T, mgr *Manager, fileID int64) (Session, *worker) {
	t.Helper()
	session, err := mgr.StartSession(context.Background(), StartRequest{
		File:     MediaFile{ID: fileID, Container: "matroska", DurationS: 1000},
		Decision: Decision{Mode: ModeHLS, Reason: ReasonVideoCodec, Tier: TierFullTranscode},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	w, err := mgr.workerForSession(session.ID)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	return session, w
}

func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

// A request that looked the worker up before its session ended must not start
// ffmpeg on it: the manager has forgotten the worker, so nothing would ever
// stop that process, and it would hold a transcode slot until end of file.
func TestClosedWorkerNeverStartsFFmpeg(t *testing.T) {
	mgr := NewManager(Options{CacheDir: t.TempDir(), FFmpeg: sleepFFmpeg(t), MaxVideoWorkers: 1})
	session, w := startTranscodeSession(t, mgr, 1)
	mgr.EndSession(session.ID)

	if err := w.ensureSegment(context.Background(), 10, w.dir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ensureSegment on closed worker = %v, want ErrNotFound", err)
	}
	if w.running || len(mgr.videoSem) != 0 {
		t.Fatalf("closed worker running=%v slots=%d, want idle with no slot", w.running, len(mgr.videoSem))
	}
}

// With every transcode slot taken, a request waiting for one must stall only
// itself. It used to wait holding the worker lock, so the reaper (holding the
// manager lock) queued behind it and every playback request on the server froze.
func TestSlotWaitDoesNotBlockManager(t *testing.T) {
	mgr := NewManager(Options{CacheDir: t.TempDir(), FFmpeg: sleepFFmpeg(t), MaxVideoWorkers: 1})
	sessionA, a := startTranscodeSession(t, mgr, 1)
	sessionB, b := startTranscodeSession(t, mgr, 2)

	within(t, "first transcode start", func() {
		if err := a.ensureRunning(context.Background(), 0); err != nil {
			t.Errorf("start A: %v", err)
		}
	})
	within(t, "seek restart holding the worker's own slot", func() {
		if err := a.ensureSegment(context.Background(), 100, a.dir); err != nil {
			t.Errorf("restart A: %v", err)
		}
	})
	// The killed pre-seek process exiting must not be mistaken for the new one:
	// that dropped the new process's cancel func and let the next request start
	// a second ffmpeg into the same directory.
	time.Sleep(200 * time.Millisecond)
	a.mu.Lock()
	tracked := a.running && a.cmdCancel != nil && a.semHeld
	a.mu.Unlock()
	if !tracked {
		t.Fatal("worker lost track of its restarted ffmpeg once the superseded process exited")
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- b.ensureRunning(context.Background(), 0) }()
	time.Sleep(100 * time.Millisecond)

	within(t, "manager calls while B waits for a slot", func() {
		mgr.reapIdle(time.Now())
		mgr.ActiveSessions()
		if _, err := mgr.Playlist(context.Background(), sessionB.ID); err != nil {
			t.Errorf("playlist B: %v", err)
		}
	})
	within(t, "ending the waiting session", func() { mgr.EndSession(sessionB.ID) })
	select {
	case err := <-waitErr:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("waiting start after end = %v, want ErrNotFound", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting start not released by EndSession")
	}
	if b.running {
		t.Fatal("ended worker B started ffmpeg")
	}

	mgr.EndSession(sessionA.ID)
	if got := len(mgr.videoSem); got != 0 {
		t.Fatalf("slots held after all sessions ended = %d, want 0", got)
	}
}
