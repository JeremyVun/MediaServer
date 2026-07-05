package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/jobs"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/zeebo/xxh3"
)

func TestUploadsCreateResumeCompleteAndEnqueueProbe(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "Media A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	srv.jobs = jobs.NewManager(jobs.Options{Store: st, ThumbsDir: srv.thumbsDir})

	body := []byte("uploaded movie bytes")
	checksum := fmt.Sprintf("%016x", xxh3.Hash(body))
	createBody := fmt.Sprintf(`{"filename":"movie.mp4","size":%d,"root_id":%d,"checksum_xxh3":%q}`,
		len(body), root.ID, checksum)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/uploads", strings.NewReader(createBody))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created createUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.ChunkSize != uploadChunkSize {
		t.Fatalf("created = %+v", created)
	}

	putUploadChunk(t, srv, created.ID, 0, 4, int64(len(body)), body[:5], http.StatusOK)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/uploads/"+created.ID, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status uploadStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Received != 5 || status.Size != int64(len(body)) || status.Status != "active" {
		t.Fatalf("status = %+v", status)
	}

	rec = putUploadChunk(t, srv, created.ID, 0, 1, int64(len(body)), body[:2], http.StatusConflict)
	var mismatch uploadOffsetError
	if err := json.Unmarshal(rec.Body.Bytes(), &mismatch); err != nil {
		t.Fatalf("decode mismatch: %v", err)
	}
	if mismatch.Error.Code != "offset_mismatch" || mismatch.Expected != 5 {
		t.Fatalf("mismatch = %+v", mismatch)
	}

	putUploadChunk(t, srv, created.ID, 5, int64(len(body)-1), int64(len(body)), body[5:], http.StatusOK)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/uploads/"+created.ID+"/complete", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var complete uploadCompleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &complete); err != nil {
		t.Fatalf("decode complete: %v", err)
	}
	if complete.ItemID != nil {
		t.Fatalf("complete item_id = %v, want nil before probe", *complete.ItemID)
	}

	finalPath := filepath.Join(rootPath, "incoming", "movie.mp4")
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("final bytes = %q, want %q", got, body)
	}
	if _, err := os.Stat(uploadPartPath(rootPath, created.ID)); !os.IsNotExist(err) {
		t.Fatalf("part file stat err = %v, want not exist", err)
	}
	upload, err := st.GetUpload(ctx, created.ID)
	if err != nil {
		t.Fatalf("get upload: %v", err)
	}
	if upload.Status != "complete" || upload.RelPath == nil || *upload.RelPath != "incoming/movie.mp4" {
		t.Fatalf("completed upload row = %+v", upload)
	}

	listed, err := st.ListJobs(ctx, store.ListJobsOpts{Status: "queued"})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(listed) != 1 || listed[0].Type != jobs.TypeProbe || !strings.Contains(listed[0].Payload, "incoming/movie.mp4") {
		t.Fatalf("jobs = %+v", listed)
	}
}

func TestUploadStatusReconcilesPartFile(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "Media A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	body := []byte("uploaded movie bytes")
	createBody := fmt.Sprintf(`{"filename":"movie.mp4","size":%d,"root_id":%d}`, len(body), root.ID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/uploads", strings.NewReader(createBody))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created createUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	putUploadChunk(t, srv, created.ID, 0, 9, int64(len(body)), body[:10], http.StatusOK)
	partPath := uploadPartPath(rootPath, created.ID)

	getStatus := func() uploadStatusResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/uploads/"+created.ID, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
		}
		var status uploadStatusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		return status
	}

	// Part file longer than received: truncated back down.
	if err := os.WriteFile(partPath, append(append([]byte(nil), body[:10]...), "junk"...), 0o644); err != nil {
		t.Fatalf("extend part: %v", err)
	}
	if status := getStatus(); status.Received != 10 {
		t.Fatalf("received after extend = %d, want 10", status.Received)
	}
	if info, err := os.Stat(partPath); err != nil || info.Size() != 10 {
		t.Fatalf("part size = %v %v, want 10", info, err)
	}

	// Part file shorter than received (crash before flush): received clamps
	// down instead of zero-extending the file.
	if err := os.Truncate(partPath, 4); err != nil {
		t.Fatalf("shrink part: %v", err)
	}
	if status := getStatus(); status.Received != 4 {
		t.Fatalf("received after shrink = %d, want 4", status.Received)
	}
	if info, err := os.Stat(partPath); err != nil || info.Size() != 4 {
		t.Fatalf("part size = %v %v, want 4", info, err)
	}

	// Upload still completes from the clamped offset.
	putUploadChunk(t, srv, created.ID, 4, int64(len(body)-1), int64(len(body)), body[4:], http.StatusOK)
	if status := getStatus(); status.Received != int64(len(body)) {
		t.Fatalf("received = %d, want %d", status.Received, len(body))
	}
}

func TestUploadCreateRefusesInsufficientSpace(t *testing.T) {
	srv, st := newTestServer(t)
	srv.uploadMinGB = 1_000_000
	ctx := context.Background()
	root, err := st.UpsertRoot(ctx, "Media A", t.TempDir())
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	body := `{"filename":"movie.mp4","size":1,"root_id":` + strconv.FormatInt(root.ID, 10) + `}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/uploads", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func putUploadChunk(t *testing.T, srv *Server, id string, start, end, total int64, body []byte, want int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/uploads/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("PUT %s %d-%d status=%d body=%s, want %d", id, start, end, rec.Code, rec.Body.String(), want)
	}
	return rec
}
