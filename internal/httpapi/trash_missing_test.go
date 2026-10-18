package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestRestoreAutoTrashedItemComesBackMissing(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	rootPath := t.TempDir()
	root, err := st.UpsertRoot(ctx, "Media", rootPath)
	if err != nil {
		t.Fatal(err)
	}
	item, file, err := st.CreateItemWithFile(ctx, store.NewItem{Title: "Gone"}, store.NewFile{
		RootID: root.ID, RelPath: "incoming/gone.mkv", Size: 5, Mtime: time.Now(), Fingerprint: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetFileStatus(ctx, file.ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE media_files SET missing_since = datetime('now', '-1 hour') WHERE id = ?`, file.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.TrashMissingItem(ctx, item.ID, time.Now().Add(-10*time.Minute)); err != nil || !ok {
		t.Fatalf("auto-trash: %v %v", ok, err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/restore", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.GetItem(ctx, item.ID)
	if err != nil || got.DeletedAt != nil {
		t.Fatalf("restored item = %+v %v", got, err)
	}
	gotFile, err := st.GetFile(ctx, file.ID)
	if err != nil || gotFile.Status != "missing" {
		t.Fatalf("restored file = %+v %v, want missing", gotFile, err)
	}
	var fresh int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM media_files
		WHERE id = ? AND missing_since >= datetime('now', '-1 minute')`, file.ID).Scan(&fresh); err != nil || fresh != 1 {
		t.Fatalf("restore did not restart the grace (%d, %v)", fresh, err)
	}
	for _, p := range []string{filepath.Join(rootPath, ".trash"), filepath.Join(rootPath, "incoming")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("restore touched %s: %v", p, err)
		}
	}
}
