package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/classify"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestPurgingLastEpisodeRemovesShowArt(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	srv.classifier = classify.New(classify.Options{Store: st, Bus: srv.bus, ArtDir: t.TempDir()})
	evs, cancel := srv.bus.Subscribe()
	defer cancel()

	root, err := st.UpsertRoot(ctx, "Media", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	show, err := st.CreateShow(ctx, store.NewShow{Title: "Andor"})
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := st.CreateItemWithFile(ctx, store.NewItem{Type: "episode", Title: "Kassa"}, store.NewFile{
		RootID: root.ID, RelPath: "Andor.S01E01.mkv", Size: 1, Mtime: time.Now(), Fingerprint: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateItem(ctx, item.ID, store.UpdateItemParams{
		ShowID: store.SetTo(show.ID), ClassifiedAt: store.SetTo(store.FormatTime(time.Now())),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkItemTrashed(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	art := []string{srv.classifier.PosterPath(show.ID), srv.classifier.BackdropPath(show.ID)}
	for _, p := range art {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("art"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/items/"+strconv.FormatInt(item.ID, 10)+"/purge", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("purge status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetShow(ctx, show.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("show survived: %v", err)
	}
	for _, p := range art {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("art %s left behind: %v", p, err)
		}
	}
	for {
		select {
		case e := <-evs:
			if m, ok := e.Payload.(map[string]any); ok && e.Type == events.ShowRemoved && m["id"] == show.ID {
				return
			}
		default:
			t.Fatal("no show.removed published")
		}
	}
}
