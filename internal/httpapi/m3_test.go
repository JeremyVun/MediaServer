package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/playback"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestDirectStreamRangeHeadAndIfRange(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	_, file := seedProbedItem(t, ctx, st)
	root, err := st.GetRoot(ctx, file.RootID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	path := filepath.Join(root.Path, filepath.FromSlash(file.RelPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte("0123456789abcdef")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	mtime := time.Date(2026, 7, 4, 1, 2, 3, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	url := "/api/files/" + strconv.FormatInt(file.ID, 10) + "/stream"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(body) {
		t.Fatalf("full stream status=%d body=%q", rec.Code, rec.Body.String())
	}
	lastModified := rec.Header().Get("Last-Modified")
	if lastModified == "" {
		t.Fatal("missing Last-Modified")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=2-5")
	req.Header.Set("If-Range", lastModified)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("range status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/16" {
		t.Fatalf("Content-Range = %q", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=2-5")
	req.Header.Set("If-Range", time.Date(2026, 7, 3, 1, 2, 3, 0, time.UTC).Format(http.TimeFormat))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(body) {
		t.Fatalf("stale If-Range status=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("HEAD", url, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body_len=%d", rec.Code, rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Length"); got != "16" {
		t.Fatalf("HEAD Content-Length = %q", got)
	}
}

func TestPlayReturnsHLSSessionWhenContainerNeedsRemux(t *testing.T) {
	srv, st := newTestServer(t)
	srv.playback = playback.NewManager(playback.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	item, file := seedProbedItem(t, ctx, st)
	if err := st.UpdateFileProbe(ctx, file.ID, store.ProbeResult{
		Container: "matroska",
		DurationS: 12,
		Bitrate:   1000,
		Width:     640,
		Height:    360,
	}); err != nil {
		t.Fatalf("update probe: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/play", strings.NewReader(`{
		"capabilities": {
			"containers": ["mp4"],
			"video_codecs": ["h264"],
			"audio_codecs": ["aac"],
			"max_height": 2160,
			"native_hls": true
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("play status=%d body=%s", rec.Code, rec.Body.String())
	}
	var play playResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &play); err != nil {
		t.Fatalf("decode play: %v", err)
	}
	if play.Mode != "hls" || play.Reason == nil || *play.Reason != playback.ReasonContainerUnsupported || play.SessionID == nil {
		t.Fatalf("play = %+v, want hls remux", play)
	}
	if play.URL != "/api/sessions/"+*play.SessionID+"/master.m3u8" {
		t.Fatalf("url = %q session=%q", play.URL, *play.SessionID)
	}
	if got := srv.playback.ActiveSessions(); got != 1 {
		t.Fatalf("active sessions = %d, want 1", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", play.URL, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "seg-00000.m4s") {
		t.Fatalf("playlist status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/sessions/"+*play.SessionID, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := srv.playback.ActiveSessions(); got != 0 {
		t.Fatalf("active sessions after delete = %d, want 0", got)
	}
}

func TestPlayPatchAndProgressEndpoints(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	item, file := seedProbedItem(t, ctx, st)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/play", strings.NewReader(`{
		"capabilities": {
			"containers": ["mp4"],
			"video_codecs": ["h264"],
			"audio_codecs": ["aac"],
			"max_height": 2160,
			"native_hls": true
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("play status=%d body=%s", rec.Code, rec.Body.String())
	}
	var play playResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &play); err != nil {
		t.Fatalf("decode play: %v", err)
	}
	wantURL := "/api/files/" + strconv.FormatInt(file.ID, 10) + "/stream"
	if play.Mode != "direct" || play.Reason != nil || play.URL != wantURL {
		t.Fatalf("play = %+v, want direct %s", play, wantURL)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/progress", strings.NewReader(`{"position_s": 121.5, "duration_s": 596.4}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("progress status=%d body=%s", rec.Code, rec.Body.String())
	}
	var progress progressResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &progress); err != nil {
		t.Fatalf("decode progress: %v", err)
	}
	if progress.PositionS != 121.5 || progress.Completed {
		t.Fatalf("progress = %+v", progress)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/progress", strings.NewReader(`{"position_s": 570, "duration_s": 596.4}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete progress status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &progress); err != nil {
		t.Fatalf("decode complete progress: %v", err)
	}
	if !progress.Completed {
		t.Fatalf("complete progress = %+v", progress)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/items/"+strconv.FormatInt(item.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail itemDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Progress == nil || !detail.Progress.Completed {
		t.Fatalf("detail progress = %+v", detail.Progress)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PATCH", "/api/items/"+strconv.FormatInt(item.ID, 10), strings.NewReader(`{"title":"Bunny restored","year":2009,"summary":"Updated","type":"movie"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if detail.Title != "Bunny restored" || detail.Type != "movie" || detail.Year == nil || *detail.Year != 2009 || detail.Summary == nil || *detail.Summary != "Updated" {
		t.Fatalf("patched detail = %+v", detail)
	}
	if detail.Progress == nil || !detail.Progress.Completed {
		t.Fatalf("patched detail progress = %+v", detail.Progress)
	}
	if len(detail.Files) != 1 || detail.Files[0].ID != file.ID {
		t.Fatalf("patched detail files = %+v", detail.Files)
	}
}
