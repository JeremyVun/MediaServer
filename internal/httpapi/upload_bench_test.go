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
	"strings"
	"testing"
	"time"
)

// TestUploadThroughputMeasure is a throughput probe, not an assertion; run
// with -run TestUploadThroughputMeasure -v to see MB/s through the real
// chunk handler (disk + SQLite included, network excluded). Set
// UPLOAD_BENCH_DIR to point the root at a real drive (e.g. the exFAT media
// volume) instead of t.TempDir on the internal SSD; the probe writes under a
// dot-prefixed subdirectory there (invisible to the watcher) and removes it.
func TestUploadThroughputMeasure(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput probe")
	}
	srv, st := newTestServer(t)
	ctx := context.Background()
	rootPath := t.TempDir()
	if dir := os.Getenv("UPLOAD_BENCH_DIR"); dir != "" {
		rootPath = filepath.Join(dir, fmt.Sprintf(".upload-bench-%d", os.Getpid()))
		if err := os.MkdirAll(rootPath, 0o755); err != nil {
			t.Fatalf("bench dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(rootPath) })
	}
	root, err := st.UpsertRoot(ctx, "Bench", rootPath)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	const totalSize = 512 * 1024 * 1024
	createBody := fmt.Sprintf(`{"filename":"bench.mp4","size":%d,"root_id":%d}`, totalSize, root.ID)
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

	chunk := bytes.Repeat([]byte{0xAB}, int(created.ChunkSize))
	start := time.Now()
	var offset int64
	for offset < totalSize {
		end := offset + created.ChunkSize
		if end > totalSize {
			end = totalSize
		}
		rec := httptest.NewRecorder()
		// smallReads mimics TCP body reads over Wi-Fi (a few KB per Read); the
		// handler must not let read granularity become disk-write granularity.
		req := httptest.NewRequest("PUT", "/api/uploads/"+created.ID, smallReads{bytes.NewReader(chunk[:end-offset])})
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end-1, totalSize))
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("chunk status=%d body=%s", rec.Code, rec.Body.String())
		}
		offset = end
	}
	elapsed := time.Since(start)
	t.Logf("chunk_size=%d MiB, uploaded %d MiB in %s = %.1f MB/s",
		created.ChunkSize/(1024*1024), totalSize/(1024*1024), elapsed.Round(time.Millisecond),
		float64(totalSize)/elapsed.Seconds()/1e6)
}

// smallReads caps each Read at 16 KB, the order of what a TCP socket returns
// per read on a Wi-Fi link.
type smallReads struct{ r *bytes.Reader }

func (s smallReads) Read(p []byte) (int, error) {
	if len(p) > 16*1024 {
		p = p[:16*1024]
	}
	return s.r.Read(p)
}
