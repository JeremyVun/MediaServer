package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremyVun/MediaServer/internal/classify"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeTVmaze knows one show, Andor (id 1), whose episodes aired airdate.
type fakeTVmaze struct {
	srv     *httptest.Server
	down    atomic.Bool
	airdate string
}

func newFakeTVmaze(t *testing.T) *fakeTVmaze {
	f := &fakeTVmaze{airdate: "2022-09-21"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		show := map[string]any{"id": 1, "name": "Andor", "premiered": "2022-09-21",
			"image": map[string]any{"original": f.srv.URL + "/img/poster.jpg"}}
		var body any
		switch r.URL.Path {
		case "/search/shows":
			body = []any{}
			if r.URL.Query().Get("q") == "andor" {
				body = []any{map[string]any{"score": 10.0, "show": show}}
			}
		case "/shows/1":
			body = show
		case "/shows/1/episodes":
			eps := []any{}
			for n := 1; n <= 3; n++ {
				eps = append(eps, map[string]any{"id": n, "name": fmt.Sprintf("Chapter %d", n), "season": 1, "number": n, "airdate": f.airdate})
			}
			body = eps
		case "/shows/1/images":
			body = []any{map[string]any{"type": "background", "main": true,
				"resolutions": map[string]any{"original": map[string]any{"url": f.srv.URL + "/img/bg.jpg", "width": 1920}}}}
		default:
			if strings.HasPrefix(r.URL.Path, "/img/") {
				w.Write([]byte("jpeg"))
				return
			}
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type showsFixture struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	tv    *fakeTVmaze
	cls   *classify.Service
	mgr   *Manager
	root  store.Root
	bus   *events.Bus
	evs   <-chan events.Event
	n     int
	extra Options
}

const fakeFFprobe = `#!/bin/sh
case "$*" in
  *Long.Film*) dur=7200 ;;
  *Clip*) dur=600 ;;
  *) dur=3300 ;;
esac
cat <<JSON
{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":640,"height":360}],
 "chapters":[
  {"start_time":"0.000000","end_time":"60.000000","tags":{"title":"Intro"}},
  {"start_time":"60.000000","end_time":"3050.000000","tags":{}},
  {"start_time":"3050.000000","end_time":"3300.000000","tags":{"title":"Credits"}}],
 "format":{"format_name":"matroska,webm","duration":"$dur"}}
JSON
`

func newShowsFixture(t *testing.T, extra Options) *showsFixture {
	t.Helper()
	ctx := context.Background()
	st := newTestStore(t)
	root, err := st.UpsertRoot(ctx, "Media", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tv := newFakeTVmaze(t)
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	evs, cancel := bus.Subscribe()
	t.Cleanup(cancel)
	cls := classify.New(classify.Options{
		Store: st, Bus: bus, Log: quiet, ArtDir: t.TempDir(),
		TVmaze: tvmaze.New(tvmaze.Options{BaseURL: tv.srv.URL, RateLimit: 10000, RateWindow: time.Second}),
	})
	opts := extra
	opts.Store, opts.Bus, opts.Log, opts.Classifier = st, bus, quiet, cls
	opts.FFprobe = testScript(t, "ffprobe", fakeFFprobe)
	opts.FFmpeg = testScript(t, "ffmpeg", "#!/bin/sh\neval \"out=\\${$#}\"\nprintf jpeg > \"$out\"\n")
	opts.ThumbsDir = t.TempDir()
	mgr := NewManager(opts)
	cls.SetScheduler(mgr)
	return &showsFixture{t: t, ctx: ctx, st: st, tv: tv, cls: cls, mgr: mgr, root: root, bus: bus, evs: evs}
}

// file writes a media file on disk, settled long enough ago that probe
// failures would be final.
func (f *showsFixture) file(relPath string) string {
	f.t.Helper()
	path := filepath.Join(f.root.Path, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.n++
	if err := os.WriteFile(path, []byte(fmt.Sprintf("media %d", f.n)), 0o644); err != nil {
		f.t.Fatal(err)
	}
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, settled, settled); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *showsFixture) probe(relPath string) store.Item {
	f.t.Helper()
	if err := f.mgr.handleProbe(f.ctx, f.root.ID, relPath); err != nil {
		f.t.Fatalf("probe %s: %v", relPath, err)
	}
	file, err := f.st.GetFileByLocation(f.ctx, f.root.ID, relPath)
	if err != nil {
		f.t.Fatal(err)
	}
	item, err := f.st.GetItem(f.ctx, file.ItemID)
	if err != nil {
		f.t.Fatal(err)
	}
	return item
}

func (f *showsFixture) queued(typ string) []store.Job {
	f.t.Helper()
	jobs, err := f.st.ListJobs(f.ctx, store.ListJobsOpts{Status: "queued", Limit: 200})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.Job
	for _, j := range jobs {
		if j.Type == typ {
			out = append(out, j)
		}
	}
	return out
}

func (f *showsFixture) job(id int64) store.Job {
	f.t.Helper()
	job, err := f.st.GetJob(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return job
}

func (f *showsFixture) runQueued(typ string) store.Job {
	f.t.Helper()
	jobs := f.queued(typ)
	if len(jobs) != 1 {
		f.t.Fatalf("queued %s jobs = %+v, want one", typ, jobs)
	}
	f.mgr.runJob(f.ctx, jobs[0])
	return f.job(jobs[0].ID)
}

func TestProbeWritesCreationGuessAndQueuesClassify(t *testing.T) {
	f := newShowsFixture(t, Options{})
	for _, p := range []string{"Andor S01/Andor.S01E01.mkv", "Long.Film.2010.mkv", "Short.Clip.mkv"} {
		f.file(p)
	}
	episode := f.probe("Andor S01/Andor.S01E01.mkv")
	if episode.Type != "episode" || *episode.ShowKey != "andor" || *episode.Season != 1 || *episode.Episode != 1 ||
		episode.Title != "Episode 1" || episode.ClassifiedAt != nil {
		t.Fatalf("episode = %+v", episode)
	}
	if movie := f.probe("Long.Film.2010.mkv"); movie.Type != "movie" || movie.Title != "Long Film" || movie.ShowKey != nil {
		t.Fatalf("movie = %+v", movie)
	}
	if video := f.probe("Short.Clip.mkv"); video.Type != "video" {
		t.Fatalf("video = %+v", video)
	}
	classifies := f.queued(TypeClassify)
	if len(classifies) != 3 || classifies[len(classifies)-1].Payload != fmt.Sprintf(`{"item_id":%d}`, episode.ID) {
		t.Fatalf("classify jobs = %+v", classifies)
	}

	f.mgr.runJob(f.ctx, classifies[len(classifies)-1])
	if job := f.job(classifies[len(classifies)-1].ID); job.Status != "done" {
		t.Fatalf("classify job = %+v", job)
	}
	got, _ := f.st.GetItem(f.ctx, episode.ID)
	if got.ShowID == nil || got.Title != "Chapter 1" {
		t.Fatalf("classified = %+v", got)
	}
}

func TestProbeStoresChaptersAndCompletesProgress(t *testing.T) {
	f := newShowsFixture(t, Options{})
	const rel = "Below/Below.S01E01.mkv"
	path := f.file(rel)
	info, _ := os.Stat(path)
	fingerprint, err := library.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	item, file, err := f.st.CreateItemWithFile(f.ctx, classify.Guess(rel, 3300), store.NewFile{
		RootID: f.root.ID, RelPath: rel, Size: info.Size(), Mtime: info.ModTime(), Fingerprint: fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpdateFileProbe(f.ctx, file.ID, store.ProbeResult{Container: "matroska", DurationS: 3300}); err != nil {
		t.Fatal(err)
	}
	progress, err := f.st.UpsertProgress(f.ctx, item.ID, 3100, 3300, false)
	if err != nil || progress.Completed {
		t.Fatalf("setup progress = %+v, %v; 94%% without chapters is not complete", progress, err)
	}

	f.probe(rel)
	chapters, err := f.st.ListFileChapters(f.ctx, file.ID)
	if err != nil || len(chapters) != 3 || chapters[2].Title != "Credits" || chapters[2].StartS != 3050 {
		t.Fatalf("chapters = %+v, %v", chapters, err)
	}
	pending, err := f.st.ListFilesNeedingChapters(f.ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("files needing chapters = %+v, %v", pending, err)
	}
	got, err := f.st.GetProgress(f.ctx, item.ID)
	if err != nil || !got.Completed || got.UpdatedAt != progress.UpdatedAt {
		t.Fatalf("progress = %+v, %v; want completed at the credits chapter, updated_at kept", got, err)
	}
	if thumbs := f.queued(TypeThumbnail); len(thumbs) != 0 {
		t.Fatalf("unchanged re-probe queued thumbnails: %+v", thumbs)
	}
	if classifies := f.queued(TypeClassify); len(classifies) != 0 {
		t.Fatalf("re-probe queued classify: %+v", classifies)
	}
}

func TestProbeMakesPendingLongVideoAMovie(t *testing.T) {
	f := newShowsFixture(t, Options{})
	const rel = "Long.Film.2010.mkv"
	f.file(rel)
	item, _, err := f.st.CreateItemWithFile(f.ctx, classify.Guess(rel, 0), store.NewFile{
		RootID: f.root.ID, RelPath: rel, Size: 1, Mtime: time.Now(), Fingerprint: "x",
	})
	if err != nil || item.Type != "video" {
		t.Fatalf("setup = %+v, %v", item, err)
	}
	if got := f.probe(rel); got.Type != "movie" {
		t.Fatalf("type after probe = %q, want movie", got.Type)
	}
}

func TestClassifyJobWithTVmazeDownRecoversViaShowRefresh(t *testing.T) {
	f := newShowsFixture(t, Options{})
	f.tv.down.Store(true)
	f.file("Andor.S01E02.mkv")
	item := f.probe("Andor.S01E02.mkv")

	if job := f.runQueued(TypeClassify); job.Status != "done" {
		t.Fatalf("classify with TVmaze down = %+v, want done", job)
	}
	got, _ := f.st.GetItem(f.ctx, item.ID)
	if got.ShowID == nil || got.ClassifiedAt == nil {
		t.Fatalf("item = %+v, want filed under a local show", got)
	}
	refreshes := f.queued(TypeShowRefresh)
	if len(refreshes) != 1 || refreshes[0].Payload != fmt.Sprintf(`{"show_id":%d}`, *got.ShowID) {
		t.Fatalf("show_refresh jobs = %+v", refreshes)
	}
	if runAt, _ := store.ParseTime(refreshes[0].RunAt); time.Until(runAt) < 59*time.Minute {
		t.Fatalf("show_refresh runs at %s, want an hour out", refreshes[0].RunAt)
	}

	f.mgr.runJob(f.ctx, refreshes[0])
	still := f.job(refreshes[0].ID)
	runAt, _ := store.ParseTime(still.RunAt)
	if still.Status != "queued" || still.Attempts != 0 || still.Error == nil || time.Until(runAt) < 59*time.Minute {
		t.Fatalf("refresh while down = %+v, want rescheduled an hour out without spending an attempt", still)
	}

	f.tv.down.Store(false)
	f.mgr.runJob(f.ctx, still)
	if done := f.job(still.ID); done.Status != "done" {
		t.Fatalf("refresh = %+v", done)
	}
	show, err := f.st.GetShow(f.ctx, *got.ShowID)
	if err != nil || show.TVmazeID == nil || show.NeedsCheck || show.PosterAt == nil {
		t.Fatalf("show = %+v, %v; want matched and unflagged", show, err)
	}
	if after, _ := f.st.GetItem(f.ctx, item.ID); after.Title != "Chapter 2" {
		t.Fatalf("title = %q", after.Title)
	}
}

func TestEnqueueBackfill(t *testing.T) {
	f := newShowsFixture(t, Options{})
	pending := f.create("Pending.Show.S01E01.mkv")
	classified := f.create("Done.Clip.mkv")
	if _, err := f.st.UpdateItem(f.ctx, classified.ID, store.UpdateItemParams{ClassifiedAt: store.SetTo(store.FormatTime(time.Now()))}); err != nil {
		t.Fatal(err)
	}
	scanned, err := f.st.ListFilesForItem(f.ctx, classified.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReplaceFileChapters(f.ctx, scanned[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	trashed := f.create("Trashed.Clip.mkv")
	if err := f.st.MarkItemTrashed(f.ctx, trashed.ID); err != nil {
		t.Fatal(err)
	}

	if err := f.mgr.EnqueueBackfill(f.ctx); err != nil {
		t.Fatal(err)
	}
	classifies := f.queued(TypeClassify)
	if len(classifies) != 1 || classifies[0].Payload != fmt.Sprintf(`{"item_id":%d}`, pending.ID) {
		t.Fatalf("classify jobs = %+v, want only the live unclassified item", classifies)
	}
	probes := f.queued(TypeProbe)
	if len(probes) != 1 || !strings.Contains(probes[0].Payload, "Pending.Show.S01E01.mkv") {
		t.Fatalf("probe jobs = %+v, want only the unscanned online file", probes)
	}
}

// create catalogs relPath the way a pre-upgrade library holds it: no
// probe since, so its chapters were never read.
func (f *showsFixture) create(relPath string) store.Item {
	f.t.Helper()
	f.n++
	item, _, err := f.st.CreateItemWithFile(f.ctx, classify.Guess(relPath, 0), store.NewFile{
		RootID: f.root.ID, RelPath: relPath, Size: 1, Mtime: time.Now(), Fingerprint: fmt.Sprint("fp", f.n),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return item
}

func (f *showsFixture) relFile(rel string) string {
	f.file(rel)
	return rel
}

func TestCleanupPurgeDeletesShowArt(t *testing.T) {
	f := newShowsFixture(t, Options{})
	item := f.probe(f.relFile("Andor.S01E01.mkv"))
	if job := f.runQueued(TypeClassify); job.Status != "done" {
		t.Fatalf("classify = %+v", job)
	}
	got, _ := f.st.GetItem(f.ctx, item.ID)
	showID := *got.ShowID
	poster, backdrop := f.cls.PosterPath(showID), f.cls.BackdropPath(showID)
	for _, p := range []string{poster, backdrop} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("setup: art %s: %v", p, err)
		}
	}
	if err := f.st.MarkItemTrashed(f.ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	for len(f.evs) > 0 {
		<-f.evs
	}

	if err := f.mgr.handleCleanup(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.GetShow(f.ctx, showID); err == nil {
		t.Fatal("show survived its last episode's purge")
	}
	for _, p := range []string{poster, backdrop} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("art %s left behind: %v", p, err)
		}
	}
	removed := false
	for len(f.evs) > 0 {
		e := <-f.evs
		if m, ok := e.Payload.(map[string]any); ok && e.Type == events.ShowRemoved && m["id"] == showID {
			removed = true
		}
	}
	if !removed {
		t.Fatal("no show.removed published")
	}
}

func TestAiringRefreshSweep(t *testing.T) {
	f := newShowsFixture(t, Options{AiringRefreshInterval: 7 * 24 * time.Hour})
	f.tv.airdate = time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	item := f.probe(f.relFile("Andor.S01E01.mkv"))
	f.runQueued(TypeClassify)
	got, _ := f.st.GetItem(f.ctx, item.ID)

	runCtx, stop := context.WithCancel(f.ctx)
	f.mgr.Start(runCtx)
	stop()
	f.mgr.Wait()
	sweeps := f.queued(TypeAiringRefresh)
	if len(sweeps) != 1 {
		t.Fatalf("airing sweeps = %+v", sweeps)
	}
	if runAt, _ := store.ParseTime(sweeps[0].RunAt); time.Until(runAt) < 6*24*time.Hour {
		t.Fatalf("first sweep at %s, want a week out", sweeps[0].RunAt)
	}

	f.mgr.runJob(f.ctx, sweeps[0])
	refreshes := f.queued(TypeShowRefresh)
	if len(refreshes) != 1 || refreshes[0].Payload != fmt.Sprintf(`{"show_id":%d}`, *got.ShowID) {
		t.Fatalf("show_refresh jobs = %+v", refreshes)
	}
	next := f.queued(TypeAiringRefresh)
	if len(next) != 1 || next[0].ID == sweeps[0].ID {
		t.Fatalf("sweep did not reschedule itself: %+v", next)
	}
}
