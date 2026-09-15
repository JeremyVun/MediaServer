package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/playback"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func newSettingsServer(t *testing.T, cacheDir string) (*Server, *store.Store, *playback.Manager) {
	t.Helper()
	srv, st := newTestServer(t)
	pm := playback.NewManager(playback.Options{CacheDir: cacheDir, MaxBytes: 1000})
	srv = NewServer(Options{Store: st, Bus: events.NewBus(), Version: "test", ThumbsDir: t.TempDir(), Playback: pm})
	return srv, st, pm
}

func putCacheDir(t *testing.T, srv *Server, dir string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"dir": dir})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/settings/hls-cache", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSetHLSCacheDirMovesCacheAndPersists(t *testing.T) {
	oldDir := t.TempDir()
	srv, st, pm := newSettingsServer(t, oldDir)
	ctx := context.Background()
	volume := t.TempDir()
	if _, err := st.UpsertRoot(ctx, "Media", volume); err != nil {
		t.Fatalf("root: %v", err)
	}
	// Existing cache content on the old volume, plus a stray file the server
	// does not own and must leave alone.
	if err := os.MkdirAll(filepath.Join(oldDir, "7", "abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "7", "abc", "seg-00001.m4s"), []byte("segment"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "notes.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
	var before settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &before); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if before.HLSCache.Dir != oldDir || before.HLSCache.UsedBytes != 7 || !before.HLSCache.Available || before.HLSCache.MaxBytes != 1000 {
		t.Fatalf("settings before = %+v", before)
	}

	// A hidden folder under the library volume that does not exist yet: the
	// server creates it because its parent does.
	newDir := filepath.Join(volume, ".hls")
	rec = putCacheDir(t, srv, newDir)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", rec.Code, rec.Body.String())
	}
	var after settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.HLSCache.Dir != newDir || after.HLSCache.UsedBytes != 0 || !after.HLSCache.Available {
		t.Fatalf("settings after = %+v", after)
	}
	if pm.CacheDir() != newDir {
		t.Fatalf("manager cache dir = %q", pm.CacheDir())
	}
	if saved, err := st.GetSetting(ctx, store.SettingHLSCacheDir); err != nil || saved != newDir {
		t.Fatalf("saved setting = %q err=%v", saved, err)
	}
	if info, err := os.Stat(newDir); err != nil || !info.IsDir() {
		t.Fatalf("new dir not created: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(oldDir, "7")); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old cache tree was not cleared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "notes.txt")); err != nil {
		t.Fatalf("stray file removed from old cache dir: %v", err)
	}
}

func TestSetHLSCacheDirRejectsBadFolders(t *testing.T) {
	srv, st, _ := newSettingsServer(t, t.TempDir())
	ctx := context.Background()
	volume := t.TempDir()
	if _, err := st.UpsertRoot(ctx, "Media", volume); err != nil {
		t.Fatalf("root: %v", err)
	}
	cases := []struct {
		name, dir, code string
		status          int
	}{
		{"relative", "hls", "path_not_absolute", http.StatusBadRequest},
		{"visible inside root", filepath.Join(volume, "hls"), "cache_dir_not_hidden", http.StatusConflict},
		{"root itself", volume, "cache_dir_is_root", http.StatusConflict},
		{"missing parent", filepath.Join(volume, ".missing", "deeper"), "dir_missing", http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := putCacheDir(t, srv, tc.dir)
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("status=%d code=%q body=%s", rec.Code, body.Error.Code, rec.Body.String())
			}
		})
	}
}
