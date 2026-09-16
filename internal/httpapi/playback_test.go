package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/playback"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func seedLadderItem(t *testing.T, ctx context.Context, st *store.Store) (store.Item, store.File) {
	t.Helper()
	item, file := seedProbedItem(t, ctx, st)
	if err := st.UpdateFileProbe(ctx, file.ID, store.ProbeResult{
		Container: "mov",
		DurationS: 596.4,
		Bitrate:   8000,
		Width:     1920,
		Height:    1080,
	}); err != nil {
		t.Fatalf("update probe: %v", err)
	}
	return item, file
}

func postPlay(t *testing.T, srv *Server, itemID int64, quality string) (*httptest.ResponseRecorder, playResponse) {
	t.Helper()
	body := `{"capabilities":{"containers":["mp4"],"video_codecs":["h264"],"audio_codecs":["aac"],"max_height":2160,"native_hls":true}`
	if quality != "" {
		body += `,"quality":"` + quality + `"`
	}
	body += "}"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/items/"+strconv.FormatInt(itemID, 10)+"/play", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	var play playResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &play); err != nil {
			t.Fatalf("decode play: %v", err)
		}
	}
	return rec, play
}

func TestPlayResolvesQuality(t *testing.T) {
	srv, st := newTestServer(t)
	srv.playback = playback.NewManager(playback.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	item, file := seedLadderItem(t, ctx, st)

	wantQualities := []qualityResponse{
		{ID: "1080p", Width: 1920, Height: 1080},
		{ID: "720p", Width: 1280, Height: 720},
		{ID: "480p", Width: 854, Height: 480},
		{ID: "360p", Width: 640, Height: 360},
	}

	// Omitting quality is original, and original still direct-plays.
	rec, play := postPlay(t, srv, item.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("play status=%d body=%s", rec.Code, rec.Body.String())
	}
	if play.Mode != "direct" || play.Quality != "original" {
		t.Fatalf("default play = %+v, want direct original", play)
	}
	if play.URL != "/api/files/"+strconv.FormatInt(file.ID, 10)+"/stream" {
		t.Fatalf("direct url = %q", play.URL)
	}
	if len(play.Qualities) != len(wantQualities) {
		t.Fatalf("qualities = %+v, want %+v", play.Qualities, wantQualities)
	}
	for i, want := range wantQualities {
		if play.Qualities[i] != want {
			t.Fatalf("quality %d = %+v, want %+v", i, play.Qualities[i], want)
		}
	}

	rec, auto := postPlay(t, srv, item.ID, "auto")
	if rec.Code != http.StatusOK {
		t.Fatalf("auto status=%d body=%s", rec.Code, rec.Body.String())
	}
	if auto.Mode != "hls" || auto.Reason == nil || *auto.Reason != playback.ReasonQuality || auto.Quality != "auto" {
		t.Fatalf("auto play = %+v", auto)
	}
	if auto.SessionID == nil || auto.URL != "/api/sessions/"+*auto.SessionID+"/master.m3u8" {
		t.Fatalf("auto url = %q session=%v", auto.URL, auto.SessionID)
	}

	rec, fixed := postPlay(t, srv, item.ID, "480p")
	if rec.Code != http.StatusOK {
		t.Fatalf("480p status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fixed.Quality != "480p" || fixed.Mode != "hls" {
		t.Fatalf("480p play = %+v", fixed)
	}

	rec, _ = postPlay(t, srv, item.ID, "2160p")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("junk quality status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope map[string]apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope["error"].Code != "bad_request" {
		t.Fatalf("error envelope = %+v", envelope)
	}
}

// A 640x360 file offers only 360p, so a remembered 1080p resolves down to it
// and the response says so.
func TestPlayResolvesFixedQualityDown(t *testing.T) {
	srv, st := newTestServer(t)
	srv.playback = playback.NewManager(playback.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	item, _ := seedProbedItem(t, ctx, st)

	rec, play := postPlay(t, srv, item.ID, "1080p")
	if rec.Code != http.StatusOK {
		t.Fatalf("play status=%d body=%s", rec.Code, rec.Body.String())
	}
	if play.Quality != "360p" || len(play.Qualities) != 1 || play.Qualities[0].ID != "360p" {
		t.Fatalf("play = %+v, want a single 360p rung", play)
	}
}

func TestPlayOffersNoQualitiesWithoutVideo(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	item, file := seedProbedItem(t, ctx, st)
	channels := 2
	if err := st.ReplaceFileStreams(ctx, file.ID, []store.Stream{
		{StreamIndex: 0, Kind: "audio", Codec: "aac", Channels: &channels, IsDefault: true},
	}); err != nil {
		t.Fatalf("streams: %v", err)
	}

	rec, play := postPlay(t, srv, item.ID, "auto")
	if rec.Code != http.StatusOK {
		t.Fatalf("play status=%d body=%s", rec.Code, rec.Body.String())
	}
	if play.Quality != "original" || play.Qualities == nil || len(play.Qualities) != 0 {
		t.Fatalf("play = %+v, want original with an empty quality list", play)
	}
	if !strings.Contains(rec.Body.String(), `"qualities":[]`) {
		t.Fatalf("empty qualities serialized as null: %s", rec.Body.String())
	}
}

// Go 1.22 pattern routing must send each of the three session paths to the
// right handler: the literal master playlist, a flat segment, and a rung.
func TestSessionRoutesResolveByShape(t *testing.T) {
	srv, st := newTestServer(t)
	srv.playback = playback.NewManager(playback.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	item, _ := seedLadderItem(t, ctx, st)

	_, auto := postPlay(t, srv, item.ID, "auto")
	sid := *auto.SessionID

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", auto.URL, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "#EXT-X-STREAM-INF") {
		t.Fatalf("master playlist status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+sid+"/720p/stream.m3u8", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `#EXT-X-MAP:URI="init.mp4"`) {
		t.Fatalf("rung playlist status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+sid+"/1440p/stream.m3u8", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown rung playlist status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+sid+"/1440p/seg-00000.m4s", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown rung segment status=%d body=%s", rec.Code, rec.Body.String())
	}

	// A flat segment path on a ladder session has no directory to serve from.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+sid+"/seg-00000.m4s", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("flat segment on a ladder status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRungRoutesRejectNonLadderSessions(t *testing.T) {
	srv, st := newTestServer(t)
	srv.playback = playback.NewManager(playback.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	item, _ := seedLadderItem(t, ctx, st)

	// A client with no h264 support gets today's full transcode, not a ladder.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/play", strings.NewReader(
		`{"capabilities":{"containers":["mp4"],"video_codecs":["hevc"],"audio_codecs":["aac"],"max_height":2160},"quality":"original"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	var play playResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &play); err != nil {
		t.Fatalf("decode play: %v", err)
	}
	if play.Mode != "hls" || play.SessionID == nil || play.Reason == nil || *play.Reason != playback.ReasonVideoCodec {
		t.Fatalf("play = %+v, want a full transcode session", play)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+*play.SessionID+"/720p/stream.m3u8", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("rung playlist on a flat session status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlayAudioOnly(t *testing.T) {
	srv, st := newTestServer(t)
	srv.playback = playback.NewManager(playback.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	item, _ := seedLadderItem(t, ctx, st)

	rec, play := postPlay(t, srv, item.ID, "audio")
	if rec.Code != http.StatusOK {
		t.Fatalf("audio status=%d body=%s", rec.Code, rec.Body.String())
	}
	if play.Mode != "hls" || play.Reason == nil || *play.Reason != playback.ReasonQuality || play.Quality != "audio" {
		t.Fatalf("audio play = %+v", play)
	}
	if !play.AudioOnly || len(play.Qualities) != 4 {
		t.Fatalf("audio play = %+v, want audio_only with the file's video rungs", play)
	}
	if play.SessionID == nil || play.URL != "/api/sessions/"+*play.SessionID+"/master.m3u8" {
		t.Fatalf("audio url = %q session=%v", play.URL, play.SessionID)
	}

	// The tier is a flat session: the master URL is the media playlist itself
	// and the rung routes on it are 404, like every other flat session.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", play.URL, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `#EXT-X-MAP:URI="init.mp4"`) {
		t.Fatalf("audio playlist status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "#EXT-X-STREAM-INF") {
		t.Fatalf("audio playlist is multivariant:\n%s", rec.Body.String())
	}
	for _, path := range []string{"/720p/stream.m3u8", "/720p/seg-00000.m4s"} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+*play.SessionID+path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s on an audio session status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestPlayAudioOnlyNotOffered(t *testing.T) {
	srv, st := newTestServer(t)
	srv.playback = playback.NewManager(playback.Options{CacheDir: t.TempDir()})
	ctx := context.Background()
	item, file := seedLadderItem(t, ctx, st)

	// A silent video has nothing to send as audio.
	if err := st.ReplaceFileStreams(ctx, file.ID, []store.Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
	}); err != nil {
		t.Fatalf("streams: %v", err)
	}
	rec, silent := postPlay(t, srv, item.ID, "audio")
	if rec.Code != http.StatusOK {
		t.Fatalf("silent status=%d body=%s", rec.Code, rec.Body.String())
	}
	if silent.Quality != "original" || silent.AudioOnly {
		t.Fatalf("silent video play = %+v, want original without audio only", silent)
	}

	// An audio file is already audio.
	channels := 2
	if err := st.ReplaceFileStreams(ctx, file.ID, []store.Stream{
		{StreamIndex: 0, Kind: "audio", Codec: "aac", Channels: &channels, IsDefault: true},
	}); err != nil {
		t.Fatalf("streams: %v", err)
	}
	rec, audio := postPlay(t, srv, item.ID, "audio")
	if rec.Code != http.StatusOK {
		t.Fatalf("audio file status=%d body=%s", rec.Code, rec.Body.String())
	}
	if audio.Quality != "original" || audio.AudioOnly {
		t.Fatalf("audio file play = %+v, want original without audio only", audio)
	}
	if !strings.Contains(rec.Body.String(), `"audio_only":false`) {
		t.Fatalf("audio_only missing from the response: %s", rec.Body.String())
	}
}
