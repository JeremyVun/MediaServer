package playback

import (
	"bytes"
	"context"
	"encoding/binary"
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
		Decision: Decision{Mode: ModeHLS, Reason: ReasonVideoCodec, Tier: TierFullTranscode},
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
	mgr := NewManager(Options{CacheDir: t.TempDir(), IdleTimeout: time.Second, SessionTimeout: 10 * time.Second})
	session, err := mgr.StartSession(context.Background(), StartRequest{
		File:     MediaFile{ID: 10, Container: "matroska", DurationS: 9},
		Decision: Decision{Mode: ModeHLS, Reason: ReasonVideoCodec, Tier: TierFullTranscode},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	w, err := mgr.workerForSession(session.ID)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}

	// Past idleTimeout: the ffmpeg child is stopped but the session must
	// survive — a client that buffered minutes ahead comes back for more
	// segments long after the last request (SPEC-BACKEND: "cached segments
	// remain for resume").
	w.mu.Lock()
	w.lastAccess = time.Now().Add(-2 * time.Second)
	w.mu.Unlock()
	mgr.reapIdle(time.Now())
	if got := mgr.ActiveSessions(); got != 1 {
		t.Fatalf("active sessions after idle reap = %d, want 1", got)
	}
	if _, err := mgr.Playlist(context.Background(), session.ID); err != nil {
		t.Fatalf("playlist after idle reap: %v", err)
	}

	// Past sessionTimeout: the session mapping is finally dropped.
	w.mu.Lock()
	w.lastAccess = time.Now().Add(-11 * time.Second)
	w.mu.Unlock()
	mgr.reapIdle(time.Now())
	if got := mgr.ActiveSessions(); got != 0 {
		t.Fatalf("active sessions after session reap = %d, want 0", got)
	}
	if _, err := mgr.Playlist(context.Background(), session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("playlist after session reap error = %v, want ErrNotFound", err)
	}
}

// A request just past the transcoder's newest on-disk segment waits for the
// running ffmpeg instead of killing it; a request far beyond it (a seek)
// triggers a restart at that segment.
func TestWorkerForwardWindowUsesDiskProgress(t *testing.T) {
	dir := t.TempDir()
	w := &worker{
		dir:             dir,
		ffmpeg:          filepath.Join(dir, "missing-ffmpeg"),
		segmentDuration: 4 * time.Second,
		segmentWait:     20 * time.Second,
		running:         true,
		startNumber:     0,
	}
	for n := 0; n <= 9; n++ {
		if err := os.WriteFile(filepath.Join(dir, segmentName(n)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Disk high is 9, window is 5: segment 14 waits (no restart attempted).
	if err := w.ensureSegment(context.Background(), 14, dir); err != nil {
		t.Fatalf("in-window segment should wait, got restart error: %v", err)
	}
	if !w.running {
		t.Fatal("worker should still be marked running for in-window segment")
	}

	// Segment 15 is beyond the window: a restart is attempted (and fails,
	// because the ffmpeg binary doesn't exist — that failure is the signal).
	if err := w.ensureSegment(context.Background(), 15, dir); err == nil {
		t.Fatal("out-of-window segment should attempt a restart")
	}
}

// Copy-tier HLS must never cut mid-GOP. The complete playlist is synthesized
// from source keyframes, and a seek restart must reproduce the same segment at
// the same absolute media timestamp.
func TestManagerSegmentTimelineIntegrity(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH")
	}
	tmp := t.TempDir()
	source := filepath.Join(tmp, "fixture.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x90:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "24",
		"-c:v", "libx264",
		"-x264-params", "keyint=180:min-keyint=180:scenecut=0",
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
		SegmentWait:  15 * time.Second,
		PollInterval: 50 * time.Millisecond,
	})
	session, err := mgr.StartSession(context.Background(), StartRequest{
		File:       MediaFile{ID: 7, Container: "matroska", DurationS: 24, Width: 160, Height: 90},
		SourcePath: source,
		Decision:   Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux, VideoCopy: true, AudioCopy: true},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() { mgr.EndSession(session.ID) })
	playlist, err := mgr.Playlist(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("playlist: %v", err)
	}
	for _, want := range []string{
		"#EXT-X-INDEPENDENT-SEGMENTS",
		"#EXT-X-TARGETDURATION:12",
		"#EXTINF:12.000000,",
		"seg-00000.m4s",
		"seg-00001.m4s",
	} {
		if !strings.Contains(playlist, want) {
			t.Fatalf("playlist missing %q:\n%s", want, playlist)
		}
	}
	if strings.Contains(playlist, "seg-00002.m4s") {
		t.Fatalf("playlist advertises a non-keyframe segment:\n%s", playlist)
	}

	fetch := func(name string) []byte {
		t.Helper()
		req := httptest.NewRequest("GET", session.URL, nil)
		rec := httptest.NewRecorder()
		if err := mgr.ServeRungSegment(rec, req, session.ID, "", name); err != nil {
			t.Fatalf("serve %s: %v", name, err)
		}
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("%s status=%d len=%d", name, rec.Code, rec.Body.Len())
		}
		return rec.Body.Bytes()
	}

	init := fetch("init.mp4")
	seg0 := fetch("seg-00000.m4s")
	seg1 := fetch("seg-00001.m4s")
	assertSegmentStartsWithKeyframe(t, ffprobe, tmp, init, seg0)
	assertSegmentStartsWithKeyframe(t, ffprobe, tmp, init, seg1)
	originalTFDT := firstTFDT(t, seg1)
	if originalTFDT == 0 {
		t.Fatal("seg-00001 starts at zero instead of its indexed timeline position")
	}

	// Simulate the idle reap killing ffmpeg mid-session (buffer-ahead client
	// goes quiet), remove the cached second segment, then seek straight to it.
	w, err := mgr.workerForSession(session.ID)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	w.stop()
	if err := os.Remove(filepath.Join(w.dir, segmentName(1))); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	restarted := fetch("seg-00001.m4s")
	assertSegmentStartsWithKeyframe(t, ffprobe, tmp, init, restarted)
	if got := firstTFDT(t, restarted); got != originalTFDT {
		t.Fatalf("restarted segment tfdt = %d, want original %d", got, originalTFDT)
	}

	// Beyond the playlist's advertised range is a 404, not a restart loop.
	req := httptest.NewRequest("GET", session.URL, nil)
	rec := httptest.NewRecorder()
	if err := mgr.ServeRungSegment(rec, req, session.ID, "", "seg-00002.m4s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("segment past end error = %v, want ErrNotFound", err)
	}
}

func assertSegmentStartsWithKeyframe(t *testing.T, ffprobe, dir string, init, segment []byte) {
	t.Helper()
	initPath := filepath.Join(dir, "inspect-init.mp4")
	segmentPath := filepath.Join(dir, "inspect-segment.m4s")
	if err := os.WriteFile(initPath, init, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(segmentPath, segment, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffprobe,
		"-v", "error",
		"-show_entries", "packet=codec_type,flags",
		"-of", "compact=p=0:nk=0",
		"concat:"+initPath+"|"+segmentPath,
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("inspect segment packets: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "codec_type=video") {
			if !strings.Contains(line, "flags=K") {
				t.Fatalf("segment begins with a non-keyframe packet: %s", line)
			}
			return
		}
	}
	t.Fatal("segment contains no video packet")
}

// firstTFDT returns the baseMediaDecodeTime of the first traf in an fMP4
// segment (the video track as muxed here).
func firstTFDT(t *testing.T, data []byte) uint64 {
	t.Helper()
	i := bytes.Index(data, []byte("tfdt"))
	if i < 0 || i+16 > len(data) {
		t.Fatal("segment has no tfdt box")
	}
	if version := data[i+4]; version == 1 {
		return binary.BigEndian.Uint64(data[i+8 : i+16])
	}
	return uint64(binary.BigEndian.Uint32(data[i+8 : i+12]))
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
	if err := mgr.ServeRungSegment(rec, req, session.ID, "", "init.mp4"); err != nil {
		t.Fatalf("serve init: %v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("init status=%d len=%d", rec.Code, rec.Body.Len())
	}

	req = httptest.NewRequest("GET", session.URL, nil)
	rec = httptest.NewRecorder()
	if err := mgr.ServeRungSegment(rec, req, session.ID, "", "seg-00000.m4s"); err != nil {
		t.Fatalf("serve segment: %v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("segment status=%d len=%d", rec.Code, rec.Body.Len())
	}
}

func ladderStartRequest(file MediaFile, source string, quality string) StartRequest {
	streams := []Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
		{StreamIndex: 1, Kind: "audio", Codec: "aac", IsDefault: true},
	}
	caps := Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 1080}
	decision, err := DecideQuality(quality, file, streams, caps, nil, nil)
	if err != nil {
		panic(err)
	}
	return StartRequest{File: file, SourcePath: source, Streams: streams, Capabilities: caps, Decision: decision}
}

func TestManagerLadderPlaylistsAndRungRoutes(t *testing.T) {
	mgr := NewManager(Options{CacheDir: t.TempDir(), SegmentDuration: 4 * time.Second})
	ctx := context.Background()
	session, err := mgr.StartSession(ctx, ladderStartRequest(
		MediaFile{ID: 11, Container: "matroska", DurationS: 9, Width: 1920, Height: 1080}, "/media/movie.mkv", QualityAuto))
	if err != nil {
		t.Fatalf("start ladder session: %v", err)
	}
	t.Cleanup(func() { mgr.EndSession(session.ID) })

	master, err := mgr.Playlist(ctx, session.ID)
	if err != nil {
		t.Fatalf("master playlist: %v", err)
	}
	for _, want := range []string{"#EXT-X-INDEPENDENT-SEGMENTS", "720p/stream.m3u8", "1080p/stream.m3u8", "360p/stream.m3u8"} {
		if !strings.Contains(master, want) {
			t.Fatalf("master playlist missing %q:\n%s", want, master)
		}
	}
	if strings.Contains(master, "#EXTINF") {
		t.Fatalf("master playlist carries segments:\n%s", master)
	}

	rung, err := mgr.RungPlaylist(ctx, session.ID, "480p")
	if err != nil {
		t.Fatalf("rung playlist: %v", err)
	}
	for _, want := range []string{"#EXT-X-MAP:URI=\"init.mp4\"", "#EXT-X-TARGETDURATION:4", "seg-00000.m4s", "#EXT-X-ENDLIST"} {
		if !strings.Contains(rung, want) {
			t.Fatalf("rung playlist missing %q:\n%s", want, rung)
		}
	}

	if _, err := mgr.RungPlaylist(ctx, session.ID, "1440p"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown rung playlist error = %v, want ErrNotFound", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", session.URL, nil)
	if err := mgr.ServeRungSegment(rec, req, session.ID, "1440p", "seg-00000.m4s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown rung segment error = %v, want ErrNotFound", err)
	}
	if err := mgr.ServeRungSegment(rec, req, session.ID, "", "seg-00000.m4s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("flat segment on a ladder error = %v, want ErrNotFound", err)
	}
}

func TestManagerRungRoutesRejectNonLadderSessions(t *testing.T) {
	mgr := NewManager(Options{CacheDir: t.TempDir(), SegmentDuration: 4 * time.Second})
	ctx := context.Background()
	session, err := mgr.StartSession(ctx, StartRequest{
		File:     MediaFile{ID: 12, Container: "matroska", DurationS: 9},
		Decision: Decision{Mode: ModeHLS, Reason: ReasonVideoCodec, Tier: TierFullTranscode},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() { mgr.EndSession(session.ID) })

	if _, err := mgr.RungPlaylist(ctx, session.ID, "720p"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rung playlist on a flat session error = %v, want ErrNotFound", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", session.URL, nil)
	if err := mgr.ServeRungSegment(rec, req, session.ID, "720p", "seg-00000.m4s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rung segment on a flat session error = %v, want ErrNotFound", err)
	}
}

// One ffmpeg writes every rung, so both rungs must have their own init.mp4 and
// segment 0 on the shared 4 s grid.
func TestManagerServesLadderRungSegments(t *testing.T) {
	ffmpeg := ffmpegWithVideoToolbox(t)
	tmp := t.TempDir()
	source := filepath.Join(tmp, "fixture.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=800x450:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "8",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac",
		source,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg fixture generation failed: %v: %s", err, out)
	}

	mgr := NewManager(Options{
		CacheDir:     filepath.Join(tmp, "hls"),
		FFmpeg:       ffmpeg,
		SegmentWait:  45 * time.Second,
		PollInterval: 50 * time.Millisecond,
	})
	session, err := mgr.StartSession(context.Background(), ladderStartRequest(
		MediaFile{ID: 13, Container: "mov", DurationS: 8, Width: 800, Height: 450}, source, QualityAuto))
	if err != nil {
		t.Fatalf("start ladder session: %v", err)
	}
	t.Cleanup(func() { mgr.EndSession(session.ID) })

	w, err := mgr.workerForSession(session.ID)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	if got := len(w.req.Decision.Rungs); got != 2 {
		t.Fatalf("ladder has %d rungs, want 2", got)
	}

	for _, rung := range []string{"480p", "360p"} {
		for _, name := range []string{"init.mp4", "seg-00000.m4s"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", session.URL, nil)
			if err := mgr.ServeRungSegment(rec, req, session.ID, rung, name); err != nil {
				t.Fatalf("serve %s/%s: %v", rung, name, err)
			}
			if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
				t.Fatalf("%s/%s status=%d len=%d", rung, name, rec.Code, rec.Body.Len())
			}
		}
	}
	if _, err := os.Stat(filepath.Join(w.dir, "480p", "init.mp4")); err != nil {
		t.Fatalf("480p init.mp4 not written to its own rung directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "360p", "init.mp4")); err != nil {
		t.Fatalf("360p init.mp4 not written to its own rung directory: %v", err)
	}

	// A seek restarts the one ffmpeg at that segment for every rung.
	w.stop()
	for _, rung := range []string{"480p", "360p"} {
		if err := os.Remove(filepath.Join(w.dir, rung, segmentName(1))); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	if err := mgr.ServeRungSegment(rec, httptest.NewRequest("GET", session.URL, nil), session.ID, "360p", segmentName(1)); err != nil {
		t.Fatalf("serve 360p segment 1 after a seek: %v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("restarted 360p segment status=%d len=%d", rec.Code, rec.Body.Len())
	}
	if err := waitForFile(context.Background(), filepath.Join(w.dir, "480p", segmentName(1)), 45*time.Second, 50*time.Millisecond); err != nil {
		t.Fatalf("480p did not restart alongside 360p: %v", err)
	}
}

func ffmpegWithVideoToolbox(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	out, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil || !strings.Contains(string(out), "h264_videotoolbox") {
		t.Skip("ffmpeg has no VideoToolbox encoder")
	}
	return ffmpeg
}
