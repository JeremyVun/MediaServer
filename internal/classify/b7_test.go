package classify

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

func seasons(counts ...int) []tvEpisode {
	var eps []tvEpisode
	for i, n := range counts {
		for e := 1; e <= n; e++ {
			eps = append(eps, tvEpisode{i + 1, e, fmt.Sprintf("S%dE%d", i+1, e), "2023-01-01"})
		}
	}
	return eps
}

func TestDecide(t *testing.T) {
	r := func(id int, name string, year int, country string) tvmaze.Result {
		return tvmaze.Result{ID: id, Name: name, Year: year, Country: country, Score: float64(100 - id)}
	}
	euphoria := []tvmaze.Result{r(1, "Euphoria", 2019, "US"), r(2, "Euphoria", 2012, "IL")}
	office := []tvmaze.Result{r(1, "The Office", 2005, "US"), r(2, "The Office", 2001, "GB")}
	cases := []struct {
		name      string
		results   []tvmaze.Result
		key       string
		year      *int
		wantID    int
		confident bool
		ties      int
	}{
		{"country code picks the US namesake", euphoria, "euphoria us", nil, 1, true, 0},
		{"uk means GB", office, "the office uk", nil, 2, true, 0},
		{"no country code leaves a tie", euphoria, "euphoria", nil, 1, false, 2},
		{"namesakes all from other countries are weak (B17)", euphoria, "euphoria au", nil, 1, false, 0},
		{"a sole match from another country is weak (B17)", euphoria[1:], "euphoria us", nil, 2, false, 0},
		{"a sole match with no country falls back to B4", []tvmaze.Result{r(3, "Euphoria", 2019, "")}, "euphoria us", nil, 3, true, 0},
		{"the key's country beats one with no country", []tvmaze.Result{r(3, "Euphoria", 2019, ""), r(1, "Euphoria", 2019, "US")}, "euphoria us", nil, 1, true, 0},
		{"year picks the namesake", euphoria, "euphoria", ptr(2012), 2, true, 0},
		{"a year matching no namesake is weak, no tie-break", euphoria, "euphoria", ptr(2022), 1, false, 0},
		{"one name match is confident (B4)", euphoria[:1], "euphoria", nil, 1, true, 0},
		{"no name match is weak", []tvmaze.Result{r(5, "Vikingane", 2016, "NO")}, "norsemen", nil, 5, false, 0},
	}
	for _, tc := range cases {
		d := decide(tc.results, tc.key, tc.year)
		if d.result.ID != tc.wantID || d.confident != tc.confident || len(d.ties) != tc.ties {
			t.Errorf("%s: got id %d confident %v ties %d, want %d %v %d",
				tc.name, d.result.ID, d.confident, len(d.ties), tc.wantID, tc.confident, tc.ties)
		}
	}
}

func TestCountryPicksEuphoria(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 28826, name: "Euphoria", premiered: "2019-06-16", network: "HBO", country: "US", episodes: seasons(8, 8)}, "euphoria")
	h.tv.add(tvShow{id: 4011, name: "Euphoria", premiered: "2012-11-11", network: "HOT3", country: "IL", episodes: seasons(10, 10)}, "euphoria")

	us := h.classify(h.item("incoming/Euphoria.US.S02E01.1080p.WEB.h264-[HashMiner].mkv", 3300).ID)
	show := h.show(*us.ShowID)
	if show.TVmazeID == nil || *show.TVmazeID != 28826 || show.NeedsCheck || show.Network == nil || *show.Network != "HBO" {
		t.Fatalf("show = %+v, want the US Euphoria, unflagged, network HBO", show)
	}
	dated := h.classify(h.item("Euphoria (2019) - S01E05 - 03 Bonnie and Clyde.mkv", 3300).ID)
	if dated.ShowID == nil || *dated.ShowID != show.ID || len(h.shows()) != 1 {
		t.Fatalf("2019 file joined %v, shows = %d; want one Euphoria", dated.ShowID, len(h.shows()))
	}
	if n := h.tv.requests("/shows/4011"); n != 0 {
		t.Fatalf("country decided, yet %d requests went to the namesake", n)
	}
}

func TestEpisodeExistenceBreaksSiloTie(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 38052, name: "Silo", premiered: "2023-05-04", episodes: seasons(10, 10)}, "silo")
	h.tv.add(tvShow{id: 9, name: "Silo", premiered: "2017-01-01", country: "SG", episodes: seasons(6)}, "silo")
	item := h.classify(h.item("Silo.S02E07.1080p.mkv", 3000).ID)
	show := h.show(*item.ShowID)
	if show.TVmazeID == nil || *show.TVmazeID != 38052 || show.NeedsCheck || item.Title != "S2E7" {
		t.Fatalf("show = %+v item = %+v, want Silo 2023 by episode existence", show, item)
	}
}

func TestAllNamesakesHaveTheEpisodeStaysWeak(t *testing.T) {
	h := newHarness(t)
	for i, year := range []string{"2026", "1981", "1955", "2012"} {
		h.tv.add(tvShow{id: 100 + i, name: "East of Eden", premiered: year + "-01-01", episodes: seasons(8)}, "east of eden")
	}
	item := h.classify(h.item("East of Eden - S01E01 - Brothers.mkv", 3000).ID)
	show := h.show(*item.ShowID)
	if show.TVmazeID != nil || !show.NeedsCheck {
		t.Fatalf("show = %+v, want unmatched and flagged when every namesake has S1E1", show)
	}
	if n := h.tv.requests("/shows/"); n != 3 {
		t.Fatalf("%d episode-list requests, want at most three", n)
	}
	if item.Title != "Brothers" {
		t.Fatalf("item title = %q", item.Title)
	}
}

func lionessShows(h *harness) {
	h.tv.add(tvShow{id: 50415, name: "Lioness", premiered: "2023-07-23", network: "Paramount+", country: "US",
		episodes: seasons(8, 8, 8), poster: true, backdrop: true}, "lioness")
	h.tv.add(tvShow{id: 77, name: "Lioness", premiered: "2021-03-01", country: "ZA", episodes: seasons(13, 13, 13)}, "lioness")
}

func TestLaterArrivalMatchesUnmatchedShow(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	first := h.classify(h.item("Lioness.S03E01.1080p.mkv", 3000).ID)
	unmatched := h.show(*first.ShowID)
	if unmatched.TVmazeID != nil || !unmatched.NeedsCheck {
		t.Fatalf("yearless first file: show = %+v, want unmatched (namesakes all have S3E1)", unmatched)
	}
	h.drain()

	second := h.classify(h.item("Lioness.2023.S03E02.1080p.mkv", 3000).ID)
	if second.ShowID == nil || *second.ShowID != unmatched.ID || len(h.shows()) != 1 {
		t.Fatalf("2023 file joined %v, shows = %d; want the same show", second.ShowID, len(h.shows()))
	}
	show := h.show(unmatched.ID)
	if show.TVmazeID == nil || *show.TVmazeID != 50415 || show.NeedsCheck || *show.Year != 2023 ||
		show.Network == nil || *show.Network != "Paramount+" || show.PosterAt == nil || show.BackdropAt == nil {
		t.Fatalf("show = %+v, want it matched to Lioness 2023 with metadata", show)
	}
	if got := h.get(first.ID); got.Title != "S3E1" {
		t.Fatalf("first item title = %q, want TVmaze's after the match", got.Title)
	}
	if second.Title != "S3E2" {
		t.Fatalf("second item title = %q", second.Title)
	}
	if a := h.alias("lioness"); a.ShowID != show.ID || a.Source != store.AliasAuto {
		t.Fatalf("alias = %+v, want it kept", a)
	}
	if !showEvent(h.drain(), events.ShowUpdated, show.ID) {
		t.Fatal("no show.updated for the match")
	}
	hits := h.tv.hits.Load()
	h.classify(h.item("Lioness.S03E03.1080p.mkv", 3000).ID)
	if h.tv.hits.Load() != hits {
		t.Fatal("an alias to a matched show still reached TVmaze")
	}
}

func TestOwnerChoicesNeverRematched(t *testing.T) {
	t.Run("owner alias", func(t *testing.T) {
		h := newHarness(t)
		lionessShows(h)
		mine := mustShowFlagged(t, h, "Lioness")
		if _, err := h.st.PutShowAlias(h.ctx, "lioness", mine.ID, store.AliasOwner); err != nil {
			t.Fatal(err)
		}
		item := h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)
		if *item.ShowID != mine.ID || h.show(mine.ID).TVmazeID != nil || h.tv.hits.Load() != 0 {
			t.Fatalf("owner alias show re-matched: %+v, hits %d", h.show(mine.ID), h.tv.hits.Load())
		}
	})
	t.Run("not on TVmaze", func(t *testing.T) {
		h := newHarness(t)
		lionessShows(h)
		first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
		if _, err := h.svc.ChangeMatch(h.ctx, *first.ShowID, Match{}); err != nil {
			t.Fatal(err)
		}
		hits := h.tv.hits.Load()
		item := h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)
		if *item.ShowID != *first.ShowID || h.show(*first.ShowID).TVmazeID != nil || h.tv.hits.Load() != hits {
			t.Fatalf("show marked Not on TVmaze re-matched: %+v", h.show(*first.ShowID))
		}
	})
}

func mustShowFlagged(t *testing.T, h *harness, title string) store.Show {
	t.Helper()
	show, err := h.st.CreateShow(h.ctx, store.NewShow{Title: title, NeedsCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	return show
}

func TestLaterArrivalMergesIntoLocalMatch(t *testing.T) {
	h := newHarness(t)
	lionessShows(h)
	matched, err := h.st.CreateShow(h.ctx, store.NewShow{Title: "Special Ops: Lioness", TVmazeID: ptr(int64(50415))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutShowAlias(h.ctx, "special ops lioness", matched.ID, store.AliasAuto); err != nil {
		t.Fatal(err)
	}
	old := h.classify(h.item("Special.Ops.Lioness.S01E01.mkv", 3000).ID)
	first := h.classify(h.item("Lioness.S03E01.mkv", 3000).ID)
	unmatched := *first.ShowID
	if unmatched == matched.ID {
		t.Fatal("yearless file attached to the local match despite the tie")
	}
	h.drain()

	second := h.classify(h.item("Lioness.2023.S03E02.mkv", 3000).ID)
	if *second.ShowID != matched.ID || *h.get(first.ID).ShowID != matched.ID || *h.get(old.ID).ShowID != matched.ID {
		t.Fatalf("items not merged into show %d: second %v first %v", matched.ID, *second.ShowID, *h.get(first.ID).ShowID)
	}
	if _, err := h.st.GetShow(h.ctx, unmatched); err == nil || len(h.shows()) != 1 {
		t.Fatalf("unmatched show %d still exists; shows = %d", unmatched, len(h.shows()))
	}
	if a := h.alias("lioness"); a.ShowID != matched.ID {
		t.Fatalf("alias = %+v, want it repointed to the local match", a)
	}
	if !showEvent(h.drain(), events.ShowRemoved, unmatched) {
		t.Fatal("no show.removed for the merged show")
	}
}

func TestLionessSeasonPackAnyOrder(t *testing.T) {
	for run := 0; run < 5; run++ {
		h := newHarness(t)
		lionessShows(h)
		paths := []string{"Lioness.S03E01.mkv"}
		for e := 2; e <= 8; e++ {
			paths = append(paths, fmt.Sprintf("Lioness.2023.S03E%02d.mkv", e))
		}
		ids := make([]int64, len(paths))
		for i, p := range paths {
			ids[i] = h.item(p, 3000).ID
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(ids))
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- h.svc.Classify(context.Background(), id)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		h.invariant()
		shows := h.shows()
		if len(shows) != 1 || shows[0].TVmazeID == nil || *shows[0].TVmazeID != 50415 || shows[0].NeedsCheck {
			t.Fatalf("run %d: shows = %+v, want one matched Lioness", run, shows)
		}
		for _, id := range ids {
			if got := h.get(id); got.ShowID == nil || *got.ShowID != shows[0].ID {
				t.Fatalf("run %d: item %d in show %v", run, id, got.ShowID)
			}
		}
	}
}

func TestBackgroundFallsThroughDeadLinks(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 1871, name: "Mr. Robot", premiered: "2015-06-24", backgrounds: []tvBackground{
		{width: 4240, dead: true}, {width: 3840, dead: true}, {width: 1920}, {width: 1280},
	}}, "mr robot")
	item := h.classify(h.item("Mr.Robot.S01E01.mkv", 3000).ID)
	show := h.show(*item.ShowID)
	if show.BackdropAt == nil {
		t.Fatalf("no backdrop: %+v", show)
	}
	if got, _ := os.ReadFile(h.svc.BackdropPath(show.ID)); string(got) != "image "+backgroundPath(1871, 2) {
		t.Fatalf("backdrop = %q, want the 1920 px background after two dead ones", got)
	}

	h = newHarness(t)
	var bgs []tvBackground
	for i := 0; i < 5; i++ {
		bgs = append(bgs, tvBackground{width: 2000 - i, dead: i < 4})
	}
	h.tv.add(tvShow{id: 6605, name: "Shigatsu wa Kimi no Uso", premiered: "2014-10-10", backgrounds: bgs}, "shigatsu wa kimi no uso")
	item = h.classify(h.item("[Group] Shigatsu wa Kimi no Uso - 01.mkv", 1400).ID)
	if show := h.show(*item.ShowID); show.BackdropAt != nil {
		t.Fatalf("backdrop cached from the fifth candidate: %+v", show)
	}
	if n := h.tv.requests("/img/bg-"); n != 4 {
		t.Fatalf("%d background downloads, want four attempts", n)
	}
}

func TestBackgroundWidthCap(t *testing.T) {
	h := newHarness(t)
	h.tv.add(tvShow{id: 44778, name: "House of the Dragon", premiered: "2022-08-21", backgrounds: []tvBackground{
		{width: 10968}, {width: 1920}, {width: 3840}, {width: 4240},
	}}, "house of the dragon")
	item := h.classify(h.item("House.of.the.Dragon.S01E01.mkv", 3600).ID)
	got, _ := os.ReadFile(h.svc.BackdropPath(*item.ShowID))
	if string(got) != "image "+backgroundPath(44778, 2) {
		t.Fatalf("backdrop = %q, want the 3840 px background", got)
	}
}

func lavfiJPEG(t *testing.T, ffmpeg string, width, height int) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), fmt.Sprintf("%dx%d.jpg", width, height))
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=s=%dx%d", width, height),
		"-frames:v", "1", "-f", "image2", "-c:v", "mjpeg", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lavfi jpeg: %v %s", err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeConfig(t *testing.T, path string) (image.Config, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return cfg, format
}

func TestArtDownscaledWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	h := newHarness(t, func(o *Options) { o.FFmpeg = ffmpeg })
	wide := lavfiJPEG(t, ffmpeg, 4000, 1000)
	smallPoster := lavfiJPEG(t, ffmpeg, 680, 1000)
	h.tv.images[backgroundPath(1, 0)] = wide
	h.tv.images["/img/poster-1.jpg"] = smallPoster
	show := andorShow()
	show.backdrop = false
	show.backgrounds = []tvBackground{{width: 4000}}
	h.tv.add(show, "andor")

	item := h.classify(h.item("Andor.S01E01.mkv", 2400).ID)
	backdrop := h.svc.BackdropPath(*item.ShowID)
	cfg, format := decodeConfig(t, backdrop)
	if cfg.Width != BackdropMaxWidth || cfg.Height != 640 || format != "jpeg" {
		t.Fatalf("backdrop = %dx%d %s, want 2560x640 jpeg", cfg.Width, cfg.Height, format)
	}
	if got, _ := os.ReadFile(h.svc.PosterPath(*item.ShowID)); !bytes.Equal(got, smallPoster) {
		t.Fatal("a poster within the cap was re-encoded")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(backdrop), ".art-*")); len(leftovers) > 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}

	h2 := newHarness(t, func(o *Options) { o.FFmpeg = ffmpeg })
	h2.tv.images["/img/poster-1.jpg"] = lavfiJPEG(t, ffmpeg, 3000, 4500)
	h2.tv.add(andorShow(), "andor")
	item = h2.classify(h2.item("Andor.S01E01.mkv", 2400).ID)
	if cfg, format := decodeConfig(t, h2.svc.PosterPath(*item.ShowID)); cfg.Width != PosterMaxWidth || cfg.Height != 1020 || format != "jpeg" {
		t.Fatalf("oversized poster = %dx%d %s, want 680x1020 jpeg", cfg.Width, cfg.Height, format)
	}
}

func TestDownscaleFailureTriesNextBackground(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.FFmpeg = filepath.Join(t.TempDir(), "no-ffmpeg") })
	var wide bytes.Buffer
	if err := jpeg.Encode(&wide, image.NewGray(image.Rect(0, 0, 5000, 100)), nil); err != nil {
		t.Fatal(err)
	}
	h.tv.images[backgroundPath(3, 0)] = wide.Bytes()
	h.tv.add(tvShow{id: 3, name: "Below", premiered: "2026-01-01", backgrounds: []tvBackground{
		{width: 5000, main: true}, {width: 1920},
	}}, "below")
	item := h.classify(h.item("Below.S01E01.mkv", 3000).ID)
	if got, _ := os.ReadFile(h.svc.BackdropPath(*item.ShowID)); string(got) != "image "+backgroundPath(3, 1) {
		t.Fatalf("backdrop = %q, want the 1920 px fallback", got)
	}
}
