package jobs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/classify"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tmdb"
)

// fakeTMDB knows one movie, Long Film (2010), id 10.
type fakeTMDB struct {
	srv  *httptest.Server
	down atomic.Bool
	hits atomic.Int64
}

func newFakeTMDB(t *testing.T) *fakeTMDB {
	f := &fakeTMDB{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/img/") {
			w.Write([]byte("jpeg"))
			return
		}
		results := []any{}
		if r.URL.Query().Get("query") == "Long Film" && r.URL.Query().Get("year") == "2010" {
			results = append(results, map[string]any{"id": 10, "title": "Long Film", "release_date": "2010-03-01",
				"overview": "It is long.", "backdrop_path": "/long.jpg"})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newMovieFixture(t *testing.T, token string) (*showsFixture, *fakeTMDB) {
	tm := newFakeTMDB(t)
	f := newShowsFixtureWith(t, Options{}, func(o *classify.Options) {
		o.TMDB = tmdb.New(tmdb.Options{BaseURL: tm.srv.URL, ImageBaseURL: tm.srv.URL + "/img/", Token: token, RateLimit: 10000})
	})
	return f, tm
}

func TestMovieRefreshJobRetriesAnOutage(t *testing.T) {
	f, tm := newMovieFixture(t, "dummy-token")
	f.file("Long.Film.2010.mkv")
	item := f.probe("Long.Film.2010.mkv")
	tm.down.Store(true)
	if job := f.runQueued(TypeClassify); job.Status != "done" {
		t.Fatalf("classify job = %+v; a TMDB outage must not fail it", job)
	}
	refreshes := f.queued(TypeMovieRefresh)
	if len(refreshes) != 1 || refreshes[0].Payload != fmt.Sprintf(`{"item_id":%d}`, item.ID) {
		t.Fatalf("movie refresh jobs = %+v", refreshes)
	}
	runAt, _ := store.ParseTime(refreshes[0].RunAt)
	if time.Until(runAt) < 50*time.Minute {
		t.Errorf("movie refresh runs at %s, want an hour out", refreshes[0].RunAt)
	}

	f.mgr.runJob(f.ctx, refreshes[0])
	job := f.job(refreshes[0].ID)
	if job.Status != "queued" || job.Attempts != 0 {
		t.Fatalf("refresh during outage = %+v, want requeued without spending an attempt", job)
	}
	if runAt, _ := store.ParseTime(job.RunAt); time.Until(runAt) < 50*time.Minute {
		t.Errorf("retry at %s, want an hour out", job.RunAt)
	}

	tm.down.Store(false)
	f.mgr.runJob(f.ctx, job)
	if job := f.job(job.ID); job.Status != "done" {
		t.Fatalf("refresh after recovery = %+v", job)
	}
	got, _ := f.st.GetItem(f.ctx, item.ID)
	if got.TMDBID == nil || *got.TMDBID != 10 || got.ArtAt == nil || got.Summary == nil || *got.Summary != "It is long." {
		t.Fatalf("item = %+v", got)
	}
}

func TestMovieBackfill(t *testing.T) {
	for _, token := range []string{"dummy-token", ""} {
		t.Run(fmt.Sprintf("token=%v", token != ""), func(t *testing.T) {
			f, tm := newMovieFixture(t, token)
			movie := f.create("Long.Film.2010.mkv")
			checked := f.create("Other.Film.2011.mkv")
			unclassified := f.create("Third.Film.2012.mkv")
			typ, now := "movie", store.SetTo(store.FormatTime(time.Now()))
			for _, it := range []store.Item{movie, checked, unclassified} {
				p := store.UpdateItemParams{Type: &typ}
				if it.ID != unclassified.ID {
					p.ClassifiedAt = now
				}
				if it.ID == checked.ID {
					p.TMDBCheckedAt = now
				}
				if _, err := f.st.UpdateItem(f.ctx, it.ID, p); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.mgr.EnqueueBackfill(f.ctx); err != nil {
				t.Fatal(err)
			}
			refreshes := f.queued(TypeMovieRefresh)
			if token == "" {
				if len(refreshes) != 0 {
					t.Fatalf("movie refreshes without a token: %+v", refreshes)
				}
				if tm.hits.Load() != 0 {
					t.Fatal("requests without a token")
				}
				return
			}
			if len(refreshes) != 1 || refreshes[0].Payload != fmt.Sprintf(`{"item_id":%d}`, movie.ID) {
				t.Fatalf("movie refreshes = %+v, want only the classified, unchecked movie", refreshes)
			}
		})
	}
}
