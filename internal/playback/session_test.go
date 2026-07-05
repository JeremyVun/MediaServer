package playback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerPlaylistAndLifecycle(t *testing.T) {
	mgr := NewManager(Options{CacheDir: t.TempDir(), SegmentDuration: 4 * time.Second})
	session, err := mgr.StartSession(context.Background(), StartRequest{
		File:     MediaFile{ID: 10, Container: "matroska", DurationS: 9},
		Decision: Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if got := mgr.ActiveSessions(); got != 1 {
		t.Fatalf("active sessions = %d, want 1", got)
	}
	playlist, err := mgr.Playlist(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"seg-00000.m4s",
		"seg-00001.m4s",
		"seg-00002.m4s",
		"#EXT-X-ENDLIST",
	} {
		if !strings.Contains(playlist, want) {
			t.Fatalf("playlist missing %q:\n%s", want, playlist)
		}
	}
	mgr.EndSession(session.ID)
	if got := mgr.ActiveSessions(); got != 0 {
		t.Fatalf("active sessions after end = %d, want 0", got)
	}
	if _, err := mgr.Playlist(context.Background(), session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("playlist after end error = %v, want ErrNotFound", err)
	}
}

func TestManagerPruneCacheLRU(t *testing.T) {
	cache := t.TempDir()
	oldDir := filepath.Join(cache, "1", "old")
	newDir := filepath.Join(cache, "1", "new")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "seg-00000.m4s"), []byte(strings.Repeat("a", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "seg-00000.m4s"), []byte(strings.Repeat("b", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(oldDir, "seg-00000.m4s"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(Options{CacheDir: cache, MaxBytes: 25})
	if err := mgr.PruneCache(context.Background()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old cache stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("new cache removed: %v", err)
	}
}

func TestManagerReapsIdleSessions(t *testing.T) {
	mgr := NewManager(Options{CacheDir: t.TempDir(), IdleTimeout: time.Second})
	session, err := mgr.StartSession(context.Background(), StartRequest{
		File:     MediaFile{ID: 10, Container: "matroska", DurationS: 9},
		Decision: Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	w, err := mgr.workerForSession(session.ID)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	w.mu.Lock()
	w.lastAccess = time.Now().Add(-2 * time.Second)
	w.mu.Unlock()

	mgr.reapIdle(time.Now())
	if got := mgr.ActiveSessions(); got != 0 {
		t.Fatalf("active sessions = %d, want 0", got)
	}
	if _, err := mgr.Playlist(context.Background(), session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("playlist after reap error = %v, want ErrNotFound", err)
	}
}

func TestManagerServesGeneratedHLSSegment(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	tmp := t.TempDir()
	source := filepath.Join(tmp, "fixture.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x90:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "3",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		source,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg fixture generation failed: %v: %s", err, out)
	}

	mgr := NewManager(Options{
		CacheDir:     filepath.Join(tmp, "hls"),
		FFmpeg:       ffmpeg,
		SegmentWait:  10 * time.Second,
		PollInterval: 50 * time.Millisecond,
	})
	session, err := mgr.StartSession(context.Background(), StartRequest{
		File:       MediaFile{ID: 99, Container: "matroska", DurationS: 3, Width: 160, Height: 90},
		SourcePath: source,
		Decision:   Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux, VideoCopy: true, AudioCopy: true},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() { mgr.EndSession(session.ID) })

	req := httptest.NewRequest("GET", session.URL, nil)
	rec := httptest.NewRecorder()
	if err := mgr.ServeSegment(rec, req, session.ID, "init.mp4"); err != nil {
		t.Fatalf("serve init: %v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("init status=%d len=%d", rec.Code, rec.Body.Len())
	}

	req = httptest.NewRequest("GET", session.URL, nil)
	rec = httptest.NewRecorder()
	if err := mgr.ServeSegment(rec, req, session.ID, "seg-00000.m4s"); err != nil {
		t.Fatalf("serve segment: %v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("segment status=%d len=%d", rec.Code, rec.Body.Len())
	}
}
