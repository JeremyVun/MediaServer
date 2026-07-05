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

	"github.com/JeremyVun/MediaServer/internal/jobs"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/thumbs"
)

func TestLibraryListSearchDetailAndThumb(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	item, file := seedProbedItem(t, ctx, st)
	if err := os.WriteFile(thumbs.Path(srv.thumbsDir, file.ID), []byte("jpeg"), 0o644); err != nil {
		t.Fatalf("write thumb: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/items?sort=title", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list itemListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if list.Items[0].Title != "Big Buck Bunny" || !list.Items[0].Available || list.Items[0].DurationS == nil {
		t.Fatalf("summary = %+v", list.Items[0])
	}
	// The thumb exists on disk, so its URL must carry a cache-busting version.
	thumbURL := list.Items[0].ThumbURL
	if !strings.HasPrefix(thumbURL, "/api/items/"+strconv.FormatInt(item.ID, 10)+"/thumb?v=") {
		t.Fatalf("thumb_url = %q, want versioned", thumbURL)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/items?collection_id=999", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("collection filter status=%d body=%s", rec.Code, rec.Body.String())
	}
	var filtered itemListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if filtered.Total != 0 || len(filtered.Items) != 0 {
		t.Fatalf("empty collection filter = %+v", filtered)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/search?q=bun", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", rec.Code, rec.Body.String())
	}
	var search itemListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if search.Total != 1 || search.Items[0].ID != item.ID {
		t.Fatalf("search = %+v", search)
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
	if detail.ID != item.ID || len(detail.Files) != 1 || len(detail.Files[0].Streams) != 2 {
		t.Fatalf("detail = %+v", detail)
	}

	// Versioned URLs are immutable-cacheable; unversioned ones (collections'
	// thumb_urls) must revalidate.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", thumbURL, nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" {
		t.Fatalf("thumb status=%d body=%q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("versioned thumb Cache-Control = %q", cc)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/thumb", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" {
		t.Fatalf("thumb status=%d body=%q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("unversioned thumb Cache-Control = %q", cc)
	}
}

func TestThumbNotReady(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	item, _ := seedProbedItem(t, ctx, st)

	// No thumb on disk yet: the summary URL stays unversioned so the browser
	// can't pin a stale entry, and the 416 must never be cached.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/items", nil)
	srv.Handler().ServeHTTP(rec, req)
	var list itemListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if want := "/api/items/" + strconv.FormatInt(item.ID, 10) + "/thumb"; list.Items[0].ThumbURL != want {
		t.Fatalf("thumb_url = %q, want %q", list.Items[0].ThumbURL, want)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/thumb", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("thumb status=%d body=%q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("not-ready thumb Cache-Control = %q", cc)
	}
}

func TestRootRescanEnqueuesReconcileJob(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "A", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	srv.jobs = jobs.NewManager(jobs.Options{Store: st, ThumbsDir: srv.thumbsDir})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/roots/"+strconv.FormatInt(root.ID, 10)+"/rescan", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rescan status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res rescanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode rescan: %v", err)
	}
	job, err := st.GetJob(ctx, res.JobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Type != jobs.TypeReconcile || job.Status != "queued" {
		t.Fatalf("job = %+v", job)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/jobs?status=queued", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("jobs status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed []jobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != job.ID {
		t.Fatalf("listed jobs = %+v", listed)
	}
}

func TestRootRescanRejectsOfflineRoot(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	root, _ := st.UpsertRoot(ctx, "A", filepath.Join(t.TempDir(), "missing"))
	if err := st.SetRootOnline(ctx, root.ID, false); err != nil {
		t.Fatalf("offline: %v", err)
	}
	srv.jobs = jobs.NewManager(jobs.Options{Store: st, ThumbsDir: srv.thumbsDir})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/roots/"+strconv.FormatInt(root.ID, 10)+"/rescan", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRetryFailedJob(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	job, err := st.EnqueueJob(ctx, jobs.TypeProbe, `{"root_id":1,"rel_path":"bad.mp4"}`)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := st.MarkJobFailed(ctx, job.ID, 1, "bad media"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/jobs/"+strconv.FormatInt(job.ID, 10)+"/retry", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != "queued" || got.Error != nil {
		t.Fatalf("retried job = %+v", got)
	}
}

func seedProbedItem(t *testing.T, ctx context.Context, st *store.Store) (store.Item, store.File) {
	t.Helper()
	root, err := st.UpsertRoot(ctx, "A", t.TempDir())
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	year := 2008
	item, err := st.CreateItem(ctx, store.NewItem{Title: "Big Buck Bunny", Year: &year})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movies/bbb.mp4",
		Size:        123,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if err := st.UpdateFileProbe(ctx, file.ID, store.ProbeResult{
		Container: "mov",
		DurationS: 596.4,
		Bitrate:   1000,
		Width:     640,
		Height:    360,
	}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	channels := 2
	if err := st.ReplaceFileStreams(ctx, file.ID, []store.Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
		{StreamIndex: 1, Kind: "audio", Codec: "aac", Channels: &channels, IsDefault: true},
	}); err != nil {
		t.Fatalf("streams: %v", err)
	}
	return item, file
}
