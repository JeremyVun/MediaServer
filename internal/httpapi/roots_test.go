package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/jobs"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestRootsAPIAddListDetachAndReattach(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	srv.jobs = jobs.NewManager(jobs.Options{Store: st, ThumbsDir: srv.thumbsDir})

	rootPath := t.TempDir()
	root := postRoot(t, srv, "Media A", rootPath, http.StatusCreated)
	if root.Name != "Media A" || root.Path != rootPath || !root.Online || root.FileCount != 0 {
		t.Fatalf("created root = %+v", root)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/roots", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var roots []rootResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &roots); err != nil {
		t.Fatalf("decode roots: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != root.ID {
		t.Fatalf("roots = %+v", roots)
	}

	postRoot(t, srv, "Duplicate", rootPath, http.StatusConflict)
	nested := filepath.Join(rootPath, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	postRoot(t, srv, "Nested", nested, http.StatusConflict)

	item, err := st.CreateItem(ctx, store.NewItem{Title: "Movie"})
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	file, err := st.CreateFile(ctx, store.NewFile{
		ItemID:      item.ID,
		RootID:      root.ID,
		RelPath:     "movie.mp4",
		Size:        10,
		Mtime:       time.Now(),
		Fingerprint: "abc",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/roots/"+strconv.FormatInt(root.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("detach status=%d body=%s", rec.Code, rec.Body.String())
	}
	gotFile, err := st.GetFile(ctx, file.ID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if gotFile.Status != "offline" {
		t.Fatalf("file status = %q, want offline", gotFile.Status)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/roots", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list after detach status=%d body=%s", rec.Code, rec.Body.String())
	}
	roots = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &roots); err != nil {
		t.Fatalf("decode roots after detach: %v", err)
	}
	if len(roots) != 0 {
		t.Fatalf("roots after detach = %+v, want none", roots)
	}

	reattached := postRoot(t, srv, "Media A", rootPath, http.StatusCreated)
	if reattached.ID != root.ID || reattached.FileCount != 1 {
		t.Fatalf("reattached root = %+v, original id %d", reattached, root.ID)
	}
}

func TestRootAddValidatesPath(t *testing.T) {
	srv, _ := newTestServer(t)

	postRoot(t, srv, "Relative", "relative/path", http.StatusBadRequest)
	postRoot(t, srv, "Missing", filepath.Join(t.TempDir(), "missing"), http.StatusBadRequest)
}

func TestRootDetachRejectsActiveUpload(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	root, err := st.UpsertRoot(ctx, "Media A", t.TempDir())
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	_, err = st.DB().ExecContext(ctx, `
		INSERT INTO uploads (id, filename, size, root_id, status)
		VALUES ('upload-1', 'movie.mp4', 10, ?, 'active')`, root.ID)
	if err != nil {
		t.Fatalf("insert upload: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/roots/"+strconv.FormatInt(root.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFSDirsListsDirectoriesOnly(t *testing.T) {
	srv, _ := newTestServer(t)
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/fs/dirs?path="+url.QueryEscape(root), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got fsDirsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode dirs: %v", err)
	}
	if got.Path != root || got.Parent == nil || *got.Parent != filepath.Dir(root) {
		t.Fatalf("path/parent = %+v", got)
	}
	if len(got.Dirs) != 2 || got.Dirs[0].Name != "alpha" || got.Dirs[1].Name != "zeta" {
		t.Fatalf("dirs = %+v", got.Dirs)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/fs/dirs?path=relative", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("relative status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func postRoot(t *testing.T, srv *Server, name, path string, wantStatus int) rootResponse {
	t.Helper()
	body := `{"name":` + strconv.Quote(name) + `,"path":` + strconv.Quote(path) + `}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/roots", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("POST /api/roots status=%d body=%s, want %d", rec.Code, rec.Body.String(), wantStatus)
	}
	if rec.Code >= 400 {
		return rootResponse{}
	}
	var root rootResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatalf("decode root: %v", err)
	}
	return root
}
