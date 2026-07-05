package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestItemDeleteRestoreAndPurgeMovesBytes(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "Media", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	livePath := filepath.Join(rootPath, "movies", "movie.mp4")
	if err := os.MkdirAll(filepath.Dir(livePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(livePath, []byte("movie"), 0o644); err != nil {
		t.Fatalf("write movie: %v", err)
	}
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movies/movie.mp4",
		Size:        5,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	trashPath := filepath.Join(rootPath, ".trash", strconv.FormatInt(file.ID, 10)+"_movie.mp4")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/items/"+strconv.FormatInt(item.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(livePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live file after delete err=%v, want not exist", err)
	}
	if got, err := os.ReadFile(trashPath); err != nil || string(got) != "movie" {
		t.Fatalf("trash file got=%q err=%v", string(got), err)
	}
	trashedItem, err := st.GetItem(ctx, item.ID)
	if err != nil || trashedItem.DeletedAt == nil {
		t.Fatalf("trashed item = %+v err=%v", trashedItem, err)
	}
	trashedFile, err := st.GetFile(ctx, file.ID)
	if err != nil || trashedFile.Status != "trashed" || trashedFile.RelPath != "movies/movie.mp4" {
		t.Fatalf("trashed file = %+v err=%v", trashedFile, err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/items?trashed=1", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trash list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var trashList itemListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &trashList); err != nil {
		t.Fatalf("decode trash list: %v", err)
	}
	if trashList.Total != 1 || trashList.Items[0].ID != item.ID {
		t.Fatalf("trash list = %+v", trashList)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/restore", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got, err := os.ReadFile(livePath); err != nil || string(got) != "movie" {
		t.Fatalf("restored file got=%q err=%v", string(got), err)
	}
	if _, err := os.Stat(trashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trash after restore err=%v, want not exist", err)
	}
	restoredItem, err := st.GetItem(ctx, item.ID)
	if err != nil || restoredItem.DeletedAt != nil {
		t.Fatalf("restored item = %+v err=%v", restoredItem, err)
	}
	restoredFile, err := st.GetFile(ctx, file.ID)
	if err != nil || restoredFile.Status != "online" {
		t.Fatalf("restored file = %+v err=%v", restoredFile, err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/items/"+strconv.FormatInt(item.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete again status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/purge", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("purge status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(trashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trash after purge err=%v, want not exist", err)
	}
	if _, err := st.GetItem(ctx, item.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("item after purge err=%v, want ErrNotFound", err)
	}
}

func TestDeleteRefusesOfflineRoot(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	root, err := st.UpsertRoot(ctx, "Offline", t.TempDir())
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := st.SetRootOnline(ctx, root.ID, false); err != nil {
		t.Fatalf("offline: %v", err)
	}
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	if _, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movie.mp4",
		Size:        5,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	}); err != nil {
		t.Fatalf("file: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/items/"+strconv.FormatInt(item.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.GetItem(ctx, item.ID)
	if err != nil || got.DeletedAt != nil {
		t.Fatalf("item changed = %+v err=%v", got, err)
	}
}
