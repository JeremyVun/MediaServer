package tvmaze

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Options{BaseURL: srv.URL, HTTPClient: srv.Client()}), srv
}

func serveFixtures(t *testing.T, routes map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := routes[r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(fixture(t, name))
	})
}

func TestSearch(t *testing.T) {
	var ua, query string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua, query = r.UserAgent(), r.URL.Query().Get("q")
		w.Write(fixture(t, "search_andor.json"))
	}))
	got, err := c.Search(context.Background(), "andor & co")
	if err != nil {
		t.Fatal(err)
	}
	if query != "andor & co" {
		t.Errorf("query = %q", query)
	}
	if !strings.HasPrefix(ua, "media-server/") {
		t.Errorf("User-Agent = %q", ua)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results", len(got))
	}
	want := Result{
		ID: 52341, Name: "Andor", Year: 2022, Score: 0.8890552, Network: "Disney+",
		PosterURL:       "https://static.tvmaze.com/uploads/images/original_untouched/564/1411766.jpg",
		PosterMediumURL: "https://static.tvmaze.com/uploads/images/medium_portrait/564/1411766.jpg",
	}
	if got[0] != want {
		t.Errorf("result[0] = %+v\nwant %+v", got[0], want)
	}
	if got[2].Network != "TBS" || got[2].Year != 2013 {
		t.Errorf("result[2] = %+v, want network TBS, year 2013", got[2])
	}
}

func TestShow(t *testing.T) {
	c, _ := newTestClient(t, serveFixtures(t, map[string]string{
		"/shows/52341":                     "show_52341.json",
		"/shows/52341/episodes?specials=1": "episodes_335_specials.json",
	}))
	s, err := c.Show(context.Background(), 52341)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 52341 || s.Name != "Andor" || s.Year != 2022 || s.Network != "Disney+" {
		t.Errorf("show = %+v", s)
	}
	if !strings.HasPrefix(s.Summary, "Andor explores a new perspective") || strings.Contains(s.Summary, "<") {
		t.Errorf("summary not stripped: %q", s.Summary)
	}
	if s.PosterURL == "" || s.PosterMediumURL == "" {
		t.Errorf("posters missing: %+v", s)
	}
	if len(s.Episodes) != 18 {
		t.Fatalf("got %d episodes, want 18", len(s.Episodes))
	}
	want := map[[2]int]Episode{
		{1, 1}: {1, 1, "A Study in Pink", "2010-07-25", 90},
		{4, 3}: {4, 3, "The Final Problem", "2017-01-15", 90},
		{0, 1}: {0, 1, "Unaired Pilot", "2010-08-30", 55},
		{0, 2}: {0, 2, "Unlocking Sherlock", "2010-08-30", 32},
		{0, 5}: {0, 5, "Unlocking Sherlock (2013)", "2013-12-25", 60},
		{0, 6}: {0, 6, "The Abominable Bride", "2016-01-01", 93},
	}
	seen := map[[2]int]bool{}
	for _, e := range s.Episodes {
		k := [2]int{e.Season, e.Number}
		if seen[k] {
			t.Errorf("duplicate episode %v", k)
		}
		seen[k] = true
		if w, ok := want[k]; ok && e != w {
			t.Errorf("episode %v = %+v, want %+v", k, e, w)
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("missing episode %v", k)
		}
	}
}

func TestSpecialsNumberAfterExistingSeasonZero(t *testing.T) {
	n := func(i int) *int { return &i }
	got := episodes([]apiEpisode{
		{ID: 9, Name: "late", Season: 2, Airdate: ""},
		{ID: 1, Name: "numbered special", Season: 0, Number: n(3)},
		{ID: 5, Name: "early", Season: 1, Airdate: "2020-01-01"},
	})
	want := []Episode{{0, 3, "numbered special", "", 0}, {0, 4, "early", "2020-01-01", 0}, {0, 5, "late", "", 0}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("episode %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestShowNotFound(t *testing.T) {
	c, _ := newTestClient(t, serveFixtures(t, nil))
	_, err := c.Show(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrNotFound only", err)
	}
}

func TestBackgrounds(t *testing.T) {
	c, _ := newTestClient(t, serveFixtures(t, map[string]string{"/shows/52341/images": "images_52341.json"}))
	got, err := c.Backgrounds(context.Background(), 52341)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 12 {
		t.Fatalf("got %d backgrounds, want the 12 backgrounds", len(got))
	}
	if want := "https://static.tvmaze.com/uploads/images/original_untouched/442/1107093.jpg"; got[0].URL != want || got[0].Width != 3840 {
		t.Errorf("first = %+v, want %q", got[0], want)
	}
}

func TestBackgroundsOrder(t *testing.T) {
	body := `[
		{"type":"background","main":false,"resolutions":{"original":{"url":"http://x/10968.jpg","width":10968}}},
		{"type":"background","main":false,"resolutions":{"original":{"url":"http://x/1920.jpg","width":1920}}},
		{"type":"background","main":false,"resolutions":{"original":{"url":"http://x/4240.jpg","width":4240}}},
		{"type":"background","main":false,"resolutions":{"original":{"url":"http://x/3840.jpg","width":3840}}},
		{"type":"background","main":true,"resolutions":{"original":{"url":"http://x/main.jpg","width":1280}}},
		{"type":"poster","main":true,"resolutions":{"original":{"url":"http://x/poster.jpg","width":9999}}}
	]`
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/shows/1/images" {
			w.Write([]byte(body))
			return
		}
		w.Write([]byte(`[{"type":"poster","main":true,"resolutions":{"original":{"url":"http://x/p.jpg","width":680}}},{"type":null,"main":false,"resolutions":{}}]`))
	}))
	got, err := c.Backgrounds(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, b := range got {
		urls = append(urls, strings.TrimPrefix(b.URL, "http://x/"))
	}
	if want := "main.jpg 3840.jpg 1920.jpg 4240.jpg 10968.jpg"; strings.Join(urls, " ") != want {
		t.Errorf("order = %v, want %s: main, widest within 3840, then the narrowest wider", urls, want)
	}
	if got, err := c.Backgrounds(context.Background(), 2); err != nil || len(got) != 0 {
		t.Errorf("Backgrounds(2) = %+v, %v; want none", got, err)
	}
}

func TestSearchCountry(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"score":0.9,"show":{"id":28826,"name":"Euphoria","premiered":"2019-06-16","network":{"name":"HBO","country":{"name":"United States","code":"US"}},"webChannel":null}},
			{"score":0.8,"show":{"id":4011,"name":"Euphoria","premiered":"2012-11-11","network":{"name":"HOT3","country":{"code":"IL"}}}},
			{"score":0.7,"show":{"id":5,"name":"Euphoria Web","webChannel":{"name":"Netflix","country":null}}}
		]`))
	}))
	got, err := c.Search(context.Background(), "euphoria")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Country != "US" || got[0].Network != "HBO" || got[1].Country != "IL" || got[2].Country != "" || got[2].Network != "Netflix" {
		t.Errorf("results = %+v", got)
	}
}

func TestMalformedJSON(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"score": 1, "show": {`))
	}))
	_, err := c.Search(context.Background(), "x")
	if err == nil || errors.Is(err, ErrUnreachable) || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want a plain decode error", err)
	}
}

func TestServerErrorIsUnreachable(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	if _, err := c.Search(context.Background(), "x"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestNetworkErrorIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c := New(Options{BaseURL: srv.URL})
	if _, err := c.Search(context.Background(), "x"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestRetryAfterThenSuccess(t *testing.T) {
	var calls atomic.Int32
	var gap time.Duration
	var first time.Time
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		gap = time.Since(first)
		w.Write([]byte(`[]`))
	}))
	if _, err := c.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || gap < time.Second {
		t.Errorf("calls = %d, gap = %s; want a retry after ≥1s", calls.Load(), gap)
	}
}

func TestRetryAfterBeyondCapIsUnreachable(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	start := time.Now()
	_, err := c.Search(context.Background(), "x")
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if calls.Load() != 1 || time.Since(start) > time.Second {
		t.Errorf("calls = %d after %s; want one call, no wait", calls.Load(), time.Since(start))
	}
}

func TestPersistent429IsUnreachable(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	if _, err := c.Search(context.Background(), "x"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if calls.Load() != maxRetries+1 {
		t.Errorf("calls = %d, want %d", calls.Load(), maxRetries+1)
	}
}

func TestRetryAfterWaitIsCancellable(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Search(ctx, "x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("cancel took %s", time.Since(start))
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for h, want := range map[string]time.Duration{
		"7":                             7 * time.Second,
		"":                              defaultRetryWait,
		"soon":                          defaultRetryWait,
		"Sun, 18 Oct 2026 12:00:04 GMT": 4 * time.Second,
	} {
		if got := retryAfter(h, now); got != want {
			t.Errorf("retryAfter(%q) = %s, want %s", h, got, want)
		}
	}
}

func TestThrottle(t *testing.T) {
	var mu sync.Mutex
	var arrivals []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	const window = 150 * time.Millisecond
	c := New(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), RateLimit: 3, RateWindow: window})

	start := time.Now()
	var wg sync.WaitGroup
	for range 7 {
		wg.Go(func() {
			if _, err := c.Search(context.Background(), "x"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(arrivals) != 7 {
		t.Fatalf("arrivals = %d", len(arrivals))
	}
	if elapsed := time.Since(start); elapsed < 2*window {
		t.Errorf("7 requests at 3 per %s took %s, want ≥ %s", window, elapsed, 2*window)
	}
}

func TestLimiterWindow(t *testing.T) {
	l := newLimiter(2, 10*time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	steps := []struct {
		at   time.Duration
		want time.Duration
	}{
		{0, 0},
		{time.Second, 0},
		{2 * time.Second, 8 * time.Second},
		{10 * time.Second, 0},
		{10 * time.Second, time.Second},
		{11 * time.Second, 0},
	}
	for i, s := range steps {
		if got := l.reserve(t0.Add(s.at)); got != s.want {
			t.Errorf("step %d at %s: wait %s, want %s", i, s.at, got, s.want)
		}
	}
}

func TestFetchImage(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.jpg":
			w.Write([]byte("jpeg bytes"))
		case "/broken.jpg":
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("partial"))
		default:
			http.NotFound(w, r)
		}
	}))
	dir := t.TempDir()
	dst := filepath.Join(dir, "posters", "1.jpg")
	if err := c.FetchImage(context.Background(), srv.URL+"/ok.jpg", dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "jpeg bytes" {
		t.Errorf("dst = %q", b)
	}

	if err := c.FetchImage(context.Background(), srv.URL+"/broken.jpg", dst); !errors.Is(err, ErrUnreachable) {
		t.Errorf("truncated body: err = %v, want ErrUnreachable", err)
	}
	if err := c.FetchImage(context.Background(), srv.URL+"/gone.jpg", dst); !errors.Is(err, ErrNotFound) {
		t.Errorf("404: err = %v, want ErrNotFound", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "jpeg bytes" {
		t.Errorf("failed fetch clobbered dst: %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
