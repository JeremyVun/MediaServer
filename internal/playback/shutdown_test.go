package playback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestShutdownReapsFFmpeg drives a real transcode, then asserts Shutdown kills
// the ffmpeg child and returns well inside the budget (no orphaned processes,
// no hang). Skips where ffmpeg is unavailable.
func TestShutdownReapsFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	tmp := t.TempDir()
	source := filepath.Join(tmp, "fixture.mp4")
	genCtx, genCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer genCancel()
	gen := exec.CommandContext(genCtx, ffmpeg,
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "30", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", source,
	)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg fixture generation failed: %v: %s", err, out)
	}

	mgr := NewManager(Options{
		CacheDir:     filepath.Join(tmp, "hls"),
		FFmpeg:       ffmpeg,
		SegmentWait:  10 * time.Second,
		PollInterval: 50 * time.Millisecond,
	})
	session, err := mgr.StartSession(context.Background(), StartRequest{
		File:       MediaFile{ID: 7, Container: "matroska", DurationS: 30, Width: 320, Height: 180},
		SourcePath: source,
		Decision:   Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux, VideoCopy: true, AudioCopy: true},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	// Serving the init segment starts the ffmpeg child.
	req := httptest.NewRequest("GET", session.URL, nil)
	rec := httptest.NewRecorder()
	if err := mgr.ServeSegment(rec, req, session.ID, "init.mp4"); err != nil {
		t.Fatalf("serve init: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("init status = %d, want 200", rec.Code)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	mgr.Shutdown(shutCtx)
	if shutCtx.Err() != nil {
		t.Fatalf("Shutdown exceeded budget after %s: %v", time.Since(start), shutCtx.Err())
	}
	if got := mgr.ActiveSessions(); got != 0 {
		t.Fatalf("active sessions after shutdown = %d, want 0", got)
	}
}
