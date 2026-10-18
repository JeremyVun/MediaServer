package tmdb

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

const testToken = "dummy-tmdb-token-7f3a9c"

type recorded struct {
	uri, auth string
}

type fakeServer struct {
	srv  *httptest.Server
	hits atomic.Int64
	mu   sync.Mutex
	reqs []recorded
}

func newFake(t *testing.T, h http.HandlerFunc) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{uri: r.URL.RequestURI(), auth: r.Header.Get("Authorization")})
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func newClient(f *fakeServer, token string, log *slog.Logger) *Client {
	return New(Options{BaseURL: f.srv.URL, ImageBaseURL: f.srv.URL + "/img/", Token: token, HTTPClient: f.srv.Client(), Log: log})
}

func debugLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

const searchBody = `{"page":1,"results":[
 {"id":438631,"title":"Dune","original_title":"Dune","release_date":"2021-09-15","overview":" Paul Atreides… ","backdrop_path":"/back.jpg","poster_path":"/post.jpg"},
 {"id":841,"title":"Dune","original_title":"Dune","release_date":"","overview":"","backdrop_path":null,"poster_path":null}
]}`

func TestSearchMovie(t *testing.T) {
	var query, year string
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		query, year = r.URL.Query().Get("query"), r.URL.Query().Get("year")
		w.Write([]byte(searchBody))
	})
	log, buf := debugLog()
	c := newClient(f, testToken, log)
	got, err := c.SearchMovie(context.Background(), "dune & co", 2021)
	if err != nil {
		t.Fatal(err)
	}
	if query != "dune & co" || year != "2021" {
		t.Errorf("query = %q, year = %q", query, year)
	}
	want := Movie{ID: 438631, Title: "Dune", OriginalTitle: "Dune", Year: 2021, Overview: "Paul Atreides…", BackdropPath: "/back.jpg", PosterPath: "/post.jpg"}
	if len(got) != 2 || got[0] != want {
		t.Fatalf("results = %+v", got)
	}
	if got[1].Year != 0 || got[1].BackdropPath != "" {
		t.Errorf("result[1] = %+v", got[1])
	}
	reqs := f.requests()
	if len(reqs) != 1 || reqs[0].auth != "Bearer "+testToken {
		t.Fatalf("requests = %+v", reqs)
	}
	if !strings.HasPrefix(reqs[0].uri, "/3/search/movie?") {
		t.Errorf("uri = %q", reqs[0].uri)
	}
	assertTokenHidden(t, reqs, buf.String())
	if !strings.Contains(buf.String(), "tmdb request") {
		t.Errorf("no debug line logged: %q", buf.String())
	}
}

func TestSearchWithoutYearOmitsIt(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["year"]; ok {
			t.Errorf("year sent: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"results":[]}`))
	})
	if _, err := newClient(f, testToken, nil).SearchMovie(context.Background(), "akira", 0); err != nil {
		t.Fatal(err)
	}
}

func TestMovie(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/149" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"id":149,"title":"Akira","original_title":"AKIRA","release_date":"1988-07-16","runtime":124,"overview":"Neo-Tokyo.","backdrop_path":"/a.jpg","poster_path":"/p.jpg"}`))
	})
	c := newClient(f, testToken, nil)
	m, err := c.Movie(context.Background(), 149)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Akira" || m.OriginalTitle != "AKIRA" || m.Year != 1988 || m.Runtime != 124 || m.BackdropPath != "/a.jpg" {
		t.Errorf("movie = %+v", m)
	}
	if _, err := c.Movie(context.Background(), 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing movie: %v, want ErrNotFound", err)
	}
}

func TestNoTokenMakesNoRequest(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(searchBody)) })
	for _, c := range []*Client{newClient(f, "", nil), newClient(f, "   ", nil)} {
		if c.Configured() {
			t.Error("client without a token reports configured")
		}
		if _, err := c.SearchMovie(context.Background(), "dune", 2021); !errors.Is(err, ErrNoToken) {
			t.Errorf("search: %v", err)
		}
		if _, err := c.Movie(context.Background(), 1); !errors.Is(err, ErrNoToken) {
			t.Errorf("movie: %v", err)
		}
		if err := c.FetchImage(context.Background(), f.srv.URL+"/img/x.jpg", filepath.Join(t.TempDir(), "x.jpg")); !errors.Is(err, ErrNoToken) {
			t.Errorf("image: %v", err)
		}
	}
	var nilClient *Client
	if nilClient.Configured() {
		t.Error("nil client reports configured")
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("%d requests without a token", n)
	}
}

func TestRetryAfterOn429(t *testing.T) {
	var calls atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(searchBody))
	})
	c := newClient(f, testToken, nil)
	start := time.Now()
	got, err := c.SearchMovie(context.Background(), "dune", 2021)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("retried after %s, want Retry-After's 1 s", elapsed)
	}
	if len(got) != 2 || calls.Load() != 2 {
		t.Errorf("got %d results after %d calls", len(got), calls.Load())
	}
}

func TestLongRetryAfterIsUnreachable(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := newClient(f, testToken, nil).SearchMovie(context.Background(), "dune", 2021)
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v, want ErrUnreachable", err)
	}
	if n := f.hits.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestServerErrorsAreUnreachable(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) })
	log, buf := debugLog()
	_, err := newClient(f, testToken, log).Movie(context.Background(), 5)
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error carries the token: %v", err)
	}
	assertTokenHidden(t, f.requests(), buf.String())

	f.srv.Close()
	_, err = newClient(f, testToken, log).Movie(context.Background(), 5)
	if !errors.Is(err, ErrUnreachable) || strings.Contains(err.Error(), testToken) {
		t.Errorf("closed server: %v", err)
	}
	if strings.Contains(buf.String(), testToken) {
		t.Error("log carries the token")
	}
}

func TestUnauthorizedIsNotNotFound(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	_, err := newClient(f, testToken, nil).SearchMovie(context.Background(), "dune", 0)
	if err == nil || errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), testToken) {
		t.Errorf("err = %v", err)
	}
}

func TestFetchImage(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img/w1280/back.jpg":
			w.Write([]byte("jpeg bytes"))
		default:
			http.NotFound(w, r)
		}
	})
	c := newClient(f, testToken, nil)
	url := c.ImageURL(BackdropSize, "/back.jpg")
	if url != f.srv.URL+"/img/w1280/back.jpg" {
		t.Fatalf("ImageURL = %q", url)
	}
	if c.ImageURL(BackdropSize, "") != "" {
		t.Error("empty path should give no URL")
	}
	dst := filepath.Join(t.TempDir(), "items", "1-backdrop.jpg")
	if err := c.FetchImage(context.Background(), url, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "jpeg bytes" {
		t.Errorf("image = %q", b)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.FetchImage(context.Background(), f.srv.URL+"/img/w1280/gone.jpg", dst); !errors.Is(err, ErrNotFound) {
		t.Errorf("dead image: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Errorf("failed download replaced the file: %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
	for _, r := range f.requests() {
		if r.auth != "" {
			t.Errorf("image request %s carried Authorization", r.uri)
		}
	}
}

func TestThrottle(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"results":[]}`)) })
	c := New(Options{BaseURL: f.srv.URL, Token: testToken, HTTPClient: f.srv.Client(), RateLimit: 2, RateWindow: 300 * time.Millisecond})
	start := time.Now()
	for range 3 {
		if _, err := c.SearchMovie(context.Background(), "x", 0); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("3 requests at 2 per 300 ms took %s", elapsed)
	}
}

func assertTokenHidden(t *testing.T, reqs []recorded, logs string) {
	t.Helper()
	for _, r := range reqs {
		if strings.Contains(r.uri, testToken) {
			t.Errorf("token in request URL %q", r.uri)
		}
	}
	if strings.Contains(logs, testToken) {
		t.Errorf("token in log output")
	}
	if strings.Contains(strings.ToLower(logs), "authorization") || strings.Contains(logs, "Bearer") {
		t.Errorf("headers logged: %q", logs)
	}
}
