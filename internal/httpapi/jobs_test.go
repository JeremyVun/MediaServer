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

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestTrashJobFileMovesUnprobedDownloadToTrash(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "Media", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	livePath := filepath.Join(rootPath, "incoming", "Dead.Show.S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(livePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(livePath, make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The file never probed, so it has no catalog row: only the failed job.
	job, err := st.EnqueueJob(ctx, "probe", `{"root_id":`+strconv.FormatInt(root.ID, 10)+`,"rel_path":"incoming/Dead.Show.S01E01.mkv"}`)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.MarkJobFailed(ctx, claimed.ID, 1, "EBML header parsing failed"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/jobs/"+strconv.FormatInt(job.ID, 10)+"/trash", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res struct {
		ItemID int64 `json:"item_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, err := os.Stat(livePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live file err=%v, want gone", err)
	}
	files, err := st.ListFilesForItem(ctx, res.ItemID)
	if err != nil || len(files) != 1 || files[0].Status != "trashed" {
		t.Fatalf("files=%+v err=%v, want one trashed file", files, err)
	}
	trashPath := filepath.Join(rootPath, ".trash", strconv.FormatInt(files[0].ID, 10)+"_Dead.Show.S01E01.mkv")
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("trash file: %v", err)
	}
	item, err := st.GetItem(ctx, res.ItemID)
	if err != nil || item.DeletedAt == nil || item.Title != "Dead Show" {
		t.Fatalf("item=%+v err=%v, want trashed with parsed title", item, err)
	}
	if _, err := st.GetJob(ctx, job.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("job err=%v, want removed", err)
	}

	// Purging trash removes the bytes and the rows, same as any item.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/trash/purge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("purge status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(trashPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trash file after purge err=%v, want gone", err)
	}
}

func TestTrashJobFileRejectsNonFailedAndFilelessJobs(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	queued, err := st.EnqueueJob(ctx, "probe", `{"root_id":1,"rel_path":"a.mkv"}`)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/jobs/"+strconv.FormatInt(queued.ID, 10)+"/trash", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("queued job status=%d body=%s", rec.Code, rec.Body.String())
	}

	cleanup, err := st.EnqueueJob(ctx, "cleanup", `{}`)
	if err != nil {
		t.Fatalf("enqueue cleanup: %v", err)
	}
	claimed, _ := st.ClaimNextJob(ctx)
	for claimed.ID != cleanup.ID {
		claimed, _ = st.ClaimNextJob(ctx)
	}
	if err := st.MarkJobFailed(ctx, cleanup.ID, 1, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/jobs/"+strconv.FormatInt(cleanup.ID, 10)+"/trash", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("cleanup job status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "job_has_no_file" {
		t.Fatalf("code=%q body=%s", body.Error.Code, rec.Body.String())
	}
}
