package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqldb)
	srv := NewServer(Options{Store: st, Bus: events.NewBus(), Version: "test", ThumbsDir: t.TempDir()})
	return srv, st
}

func TestHealthReportsRoots(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	// One root at a real path (this test's temp dir → online with free
	// space), one offline.
	onlinePath := t.TempDir()
	rootA, _ := st.UpsertRoot(ctx, "Media A", onlinePath)
	rootB, _ := st.UpsertRoot(ctx, "Media B", "/Volumes/Unplugged")
	st.SetRootOnline(ctx, rootB.ID, false)

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.Version != "test" || !got.DBOK {
		t.Errorf("version=%q db_ok=%v", got.Version, got.DBOK)
	}
	if len(got.Roots) != 2 {
		t.Fatalf("roots = %d, want 2", len(got.Roots))
	}
	byID := map[int64]healthRoot{}
	for _, r := range got.Roots {
		byID[r.ID] = r
	}
	if a := byID[rootA.ID]; !a.Online || a.FreeBytes == 0 {
		t.Errorf("online root: %+v", a)
	}
	if b := byID[rootB.ID]; b.Online || b.FreeBytes != 0 {
		t.Errorf("offline root: %+v", b)
	}
	if got.QueueDepth != 0 || got.ActiveSessions != 0 {
		t.Errorf("queue=%d sessions=%d", got.QueueDepth, got.ActiveSessions)
	}
}

func TestUnknownAPIPathReturnsJSONEnvelope(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("status = %d", rec.Code)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("bad envelope: %v (%s)", err, rec.Body.String())
	}
	if envelope.Error.Code != "not_found" {
		t.Errorf("code = %q", envelope.Error.Code)
	}
}

func TestNonAPIPathServesPlaceholderWithoutEmbed(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}
