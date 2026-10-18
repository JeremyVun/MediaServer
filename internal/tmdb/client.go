// Package tmdb is a throttled client for The Movie Database API, used for
// movie descriptions and backdrops. The read access token travels only in
// the Authorization header of API requests: never in a URL, a log line, an
// error or an image request.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL       = "https://api.themoviedb.org"
	DefaultImageBaseURL  = "https://image.tmdb.org/t/p/"
	DefaultUserAgent     = "media-server/1.0 (LAN personal media server)"
	DefaultRateLimit     = 20
	DefaultRateWindow    = time.Second
	DefaultMaxRetryAfter = 10 * time.Second
	DefaultTimeout       = 10 * time.Second

	BackdropSize  = "w1280"
	PosterSize    = "w500"
	CandidateSize = "w185"

	imageTimeout     = 60 * time.Second
	maxJSONBytes     = 8 << 20
	maxImageBytes    = 16 << 20
	maxRetries       = 2
	defaultRetryWait = 2 * time.Second
)

var (
	// ErrUnreachable covers network errors, timeouts, 5xx and a 429 that
	// outlasted the retry policy: the caller should try again later.
	ErrUnreachable = errors.New("tmdb unreachable")
	ErrNotFound    = errors.New("tmdb not found")
	// ErrNoToken: the client refuses to send anything without a token.
	ErrNoToken = errors.New("tmdb read access token not configured")

	errTooLarge = errors.New("image too large")
)

type Options struct {
	BaseURL      string
	ImageBaseURL string
	Token        string
	HTTPClient   *http.Client
	UserAgent    string
	RateLimit    int
	RateWindow   time.Duration
	// A 429 whose Retry-After exceeds this surfaces as ErrUnreachable
	// instead of being waited out in-client.
	MaxRetryAfter time.Duration
	Timeout       time.Duration
	Log           *slog.Logger
}

type Client struct {
	baseURL       string
	imageBaseURL  string
	token         string
	http          *http.Client
	userAgent     string
	maxRetryAfter time.Duration
	timeout       time.Duration
	limiter       *limiter
	log           *slog.Logger
}

func New(o Options) *Client {
	c := &Client{
		baseURL:       strings.TrimRight(o.BaseURL, "/"),
		imageBaseURL:  o.ImageBaseURL,
		token:         strings.TrimSpace(o.Token),
		http:          o.HTTPClient,
		userAgent:     o.UserAgent,
		maxRetryAfter: o.MaxRetryAfter,
		timeout:       o.Timeout,
		log:           o.Log,
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.imageBaseURL == "" {
		c.imageBaseURL = DefaultImageBaseURL
	}
	if !strings.HasSuffix(c.imageBaseURL, "/") {
		c.imageBaseURL += "/"
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.userAgent == "" {
		c.userAgent = DefaultUserAgent
	}
	if c.maxRetryAfter == 0 {
		c.maxRetryAfter = DefaultMaxRetryAfter
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	limit, window := o.RateLimit, o.RateWindow
	if limit <= 0 {
		limit = DefaultRateLimit
	}
	if window <= 0 {
		window = DefaultRateWindow
	}
	c.limiter = newLimiter(limit, window)
	return c
}

// Configured reports whether a token is set; a nil client is unconfigured.
func (c *Client) Configured() bool { return c != nil && c.token != "" }

// Movie is a search result or a movie's details. Year is 0 when TMDB has
// no release date; the paths are "" when TMDB has no such image. Runtime
// (minutes) comes only with details, and is 0 when unknown.
type Movie struct {
	ID            int
	Title         string
	OriginalTitle string
	Year          int
	Runtime       int
	Overview      string
	BackdropPath  string
	PosterPath    string
}

type apiMovie struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Runtime       int    `json:"runtime"`
	Overview      string `json:"overview"`
	BackdropPath  string `json:"backdrop_path"`
	PosterPath    string `json:"poster_path"`
}

func (m apiMovie) movie() Movie {
	out := Movie{
		ID:            m.ID,
		Title:         strings.TrimSpace(m.Title),
		OriginalTitle: strings.TrimSpace(m.OriginalTitle),
		Runtime:       m.Runtime,
		Overview:      strings.TrimSpace(m.Overview),
		BackdropPath:  m.BackdropPath,
		PosterPath:    m.PosterPath,
	}
	if len(m.ReleaseDate) >= 4 {
		out.Year, _ = strconv.Atoi(m.ReleaseDate[:4])
	}
	return out
}

// SearchMovie → /3/search/movie?query=&year= (year 0 omits it), in TMDB's
// order.
func (c *Client) SearchMovie(ctx context.Context, query string, year int) ([]Movie, error) {
	v := url.Values{"query": {query}}
	if year > 0 {
		v.Set("year", strconv.Itoa(year))
	}
	var page struct {
		Results []apiMovie `json:"results"`
	}
	if err := c.getJSON(ctx, "/3/search/movie?"+v.Encode(), &page); err != nil {
		return nil, err
	}
	out := make([]Movie, 0, len(page.Results))
	for _, r := range page.Results {
		out = append(out, r.movie())
	}
	return out, nil
}

// Movie → /3/movie/{id}.
func (c *Client) Movie(ctx context.Context, id int) (Movie, error) {
	var m apiMovie
	if err := c.getJSON(ctx, "/3/movie/"+strconv.Itoa(id), &m); err != nil {
		return Movie{}, err
	}
	return m.movie(), nil
}

// ImageURL is the CDN URL of an image path at size (BackdropSize, …), or ""
// for an empty path.
func (c *Client) ImageURL(size, path string) string {
	if path == "" {
		return ""
	}
	return c.imageBaseURL + size + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	if !c.Configured() {
		return ErrNoToken
	}
	var body []byte
	err := c.get(ctx, c.baseURL+path, c.timeout, true, func(resp *http.Response) error {
		var err error
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
		return err
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("tmdb: decode %s: %w", path, err)
	}
	return nil
}

// get issues a throttled GET and hands a 200 body to read inside the
// per-request timeout. Only API requests carry the token.
func (c *Client) get(ctx context.Context, url string, timeout time.Duration, auth bool, read func(*http.Response) error) error {
	for attempt := 0; ; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return fmt.Errorf("tmdb: GET %s: %w", url, err)
		}
		wait, err := c.attempt(ctx, url, timeout, auth, read)
		if wait == 0 {
			return err
		}
		if attempt >= maxRetries || wait > c.maxRetryAfter {
			return fmt.Errorf("tmdb: GET %s: rate limited (retry after %s): %w", url, wait, ErrUnreachable)
		}
		c.limiter.pause(wait)
	}
}

// attempt returns a non-zero wait when TMDB answered 429.
func (c *Client) attempt(ctx context.Context, url string, timeout time.Duration, auth bool, read func(*http.Response) error) (time.Duration, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("tmdb: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Debug("tmdb request", "url", url, "error", err)
		return 0, c.transportErr(ctx, url, err)
	}
	defer resp.Body.Close()
	c.log.Debug("tmdb request", "url", url, "status", resp.StatusCode)

	switch {
	case resp.StatusCode == http.StatusOK:
		if err := read(resp); err != nil {
			if errors.Is(err, errTooLarge) {
				return 0, fmt.Errorf("tmdb: GET %s: %w", url, err)
			}
			return 0, c.transportErr(ctx, url, err)
		}
		return 0, nil
	case resp.StatusCode == http.StatusNotFound:
		return 0, fmt.Errorf("tmdb: GET %s: %w", url, ErrNotFound)
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAfter(resp.Header.Get("Retry-After"), time.Now()), nil
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, fmt.Errorf("tmdb: GET %s: status 401, check tmdb.read_access_token", url)
	case resp.StatusCode >= 500:
		return 0, fmt.Errorf("tmdb: GET %s: status %d: %w", url, resp.StatusCode, ErrUnreachable)
	default:
		return 0, fmt.Errorf("tmdb: GET %s: status %d", url, resp.StatusCode)
	}
}

func (c *Client) transportErr(ctx context.Context, url string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("tmdb: GET %s: %w", url, ctx.Err())
	}
	return fmt.Errorf("tmdb: GET %s: %w: %w", url, ErrUnreachable, err)
}

func retryAfter(h string, now time.Time) time.Duration {
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return max(time.Duration(secs)*time.Second, time.Millisecond)
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(t.Sub(now), time.Millisecond)
	}
	return defaultRetryWait
}

// FetchImage downloads a CDN image to dst atomically: a partial download
// never replaces an existing file. Image requests carry no token.
func (c *Client) FetchImage(ctx context.Context, url, dst string) error {
	if !c.Configured() {
		return ErrNoToken
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tmdb: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmdb-*")
	if err != nil {
		return fmt.Errorf("tmdb: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	err = c.get(ctx, url, imageTimeout, false, func(resp *http.Response) error {
		n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxImageBytes+1))
		if err == nil && n > maxImageBytes {
			err = errTooLarge
		}
		return err
	})
	if err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tmdb: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("tmdb: %w", err)
	}
	return nil
}
