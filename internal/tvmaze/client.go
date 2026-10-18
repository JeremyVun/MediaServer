// Package tvmaze is a throttled client for the public TVmaze API.
package tvmaze

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	DefaultBaseURL       = "https://api.tvmaze.com"
	DefaultUserAgent     = "media-server/1.0 (LAN personal media server)"
	DefaultRateLimit     = 20
	DefaultRateWindow    = 10 * time.Second
	DefaultMaxRetryAfter = 10 * time.Second
	DefaultTimeout       = 10 * time.Second

	// Backgrounds run up to 8K, so an image gets longer than an API call.
	imageTimeout     = 60 * time.Second
	maxJSONBytes     = 32 << 20
	maxImageBytes    = 64 << 20
	maxRetries       = 2
	defaultRetryWait = 2 * time.Second
)

var (
	// ErrUnreachable covers network errors, timeouts, 5xx and a 429 that
	// outlasted the retry policy: the caller should try again later.
	ErrUnreachable = errors.New("tvmaze unreachable")
	ErrNotFound    = errors.New("tvmaze not found")

	errTooLarge = errors.New("image too large")
)

type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
	RateLimit  int
	RateWindow time.Duration
	// A 429 whose Retry-After exceeds this surfaces as ErrUnreachable
	// instead of being waited out in-client.
	MaxRetryAfter time.Duration
	// Timeout bounds one API request, body included (default 10 s).
	Timeout time.Duration
	Log     *slog.Logger
}

type Client struct {
	baseURL       string
	http          *http.Client
	userAgent     string
	maxRetryAfter time.Duration
	timeout       time.Duration
	limiter       *limiter
	responses     *responses
	log           *slog.Logger
}

func New(o Options) *Client {
	c := &Client{
		baseURL:       o.BaseURL,
		http:          o.HTTPClient,
		userAgent:     o.UserAgent,
		maxRetryAfter: o.MaxRetryAfter,
		timeout:       o.Timeout,
		responses:     newResponses(),
		log:           o.Log,
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
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

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	url := c.baseURL + path
	body, err := c.responses.get(ctx, url, func(ctx context.Context) ([]byte, time.Duration, error) {
		var body []byte
		var ttl time.Duration
		err := c.get(ctx, url, c.timeout, func(resp *http.Response) error {
			var err error
			body, err = io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
			if err == nil && json.Valid(body) {
				ttl = freshFor(resp.Header)
			}
			return err
		})
		return body, ttl, err
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("tvmaze: decode %s: %w", url, err)
	}
	return nil
}

// get issues a throttled GET and hands a 200 body to read. read runs inside
// the per-request timeout, so a stalled body counts as unreachable.
func (c *Client) get(ctx context.Context, url string, timeout time.Duration, read func(*http.Response) error) error {
	for attempt := 0; ; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return fmt.Errorf("tvmaze: GET %s: %w", url, err)
		}
		wait, err := c.attempt(ctx, url, timeout, read)
		if wait == 0 {
			return err
		}
		if attempt >= maxRetries || wait > c.maxRetryAfter {
			return fmt.Errorf("tvmaze: GET %s: rate limited (retry after %s): %w", url, wait, ErrUnreachable)
		}
		c.limiter.pause(wait)
	}
}

// attempt returns a non-zero wait when TVmaze answered 429.
func (c *Client) attempt(ctx context.Context, url string, timeout time.Duration, read func(*http.Response) error) (time.Duration, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("tvmaze: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Debug("tvmaze request", "url", url, "error", err)
		return 0, c.transportErr(ctx, url, err)
	}
	defer resp.Body.Close()
	c.log.Debug("tvmaze request", "url", url, "status", resp.StatusCode)

	switch {
	case resp.StatusCode == http.StatusOK:
		if err := read(resp); err != nil {
			if errors.Is(err, errTooLarge) {
				return 0, fmt.Errorf("tvmaze: GET %s: %w", url, err)
			}
			return 0, c.transportErr(ctx, url, err)
		}
		return 0, nil
	case resp.StatusCode == http.StatusNotFound:
		return 0, fmt.Errorf("tvmaze: GET %s: %w", url, ErrNotFound)
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAfter(resp.Header.Get("Retry-After"), time.Now()), nil
	case resp.StatusCode >= 500:
		return 0, fmt.Errorf("tvmaze: GET %s: status %d: %w", url, resp.StatusCode, ErrUnreachable)
	default:
		return 0, fmt.Errorf("tvmaze: GET %s: status %d", url, resp.StatusCode)
	}
}

func (c *Client) transportErr(ctx context.Context, url string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("tvmaze: GET %s: %w", url, ctx.Err())
	}
	return fmt.Errorf("tvmaze: GET %s: %w: %w", url, ErrUnreachable, err)
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

// FetchImage downloads url to dst atomically: a partial download never
// replaces an existing file.
func (c *Client) FetchImage(ctx context.Context, url, dst string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tvmaze: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tvmaze-*")
	if err != nil {
		return fmt.Errorf("tvmaze: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	err = c.get(ctx, url, imageTimeout, func(resp *http.Response) error {
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
		return fmt.Errorf("tvmaze: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("tvmaze: %w", err)
	}
	return nil
}
