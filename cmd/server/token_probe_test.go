package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const probeToken = "probe-dummy-tmdb-token-91c4e7"

// probeProxy stands in for the outside world: every outbound HTTP(S)
// request the server makes arrives here first (HTTPS_PROXY) and is refused.
type probeProxy struct {
	ln    net.Listener
	mu    sync.Mutex
	heads []string
}

func newProbeProxy(t *testing.T) *probeProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &probeProxy{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(conn)
		}
	}()
	return p
}

func (p *probeProxy) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	var head strings.Builder
	for {
		line, err := r.ReadString('\n')
		head.WriteString(line)
		if err != nil || line == "\r\n" {
			break
		}
	}
	p.mu.Lock()
	p.heads = append(p.heads, head.String())
	p.mu.Unlock()
	io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
}

func (p *probeProxy) tmdbRequests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, h := range p.heads {
		if strings.Contains(h, "themoviedb.org") || strings.Contains(h, "tmdb.org") {
			out = append(out, strings.SplitN(h, "\r\n", 2)[0])
		}
	}
	return out
}

func (p *probeProxy) all() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.heads, "\n")
}

var (
	probeBinOnce sync.Once
	probeBin     string
	probeBinErr  error
)

func buildProbeServer(t *testing.T) string {
	probeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "media-server-token-probe-")
		if err != nil {
			probeBinErr = err
			return
		}
		probeBin = filepath.Join(dir, "media-server")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, "go", "build", "-o", probeBin, ".").CombinedOutput()
		if err != nil {
			probeBinErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if probeBinErr != nil {
		t.Fatal(probeBinErr)
	}
	return probeBin
}

type probeRun struct {
	t       *testing.T
	base    string
	dataDir string
	output  *bytes.Buffer
	proxy   *probeProxy
	bodies  []string
}

// startProbeServer runs the real binary with a lavfi movie in its only root.
func startProbeServer(t *testing.T, tokenYAML string, extraArgs ...string) *probeRun {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	bin := buildProbeServer(t)
	dir := t.TempDir()
	root := filepath.Join(dir, "media")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	movie := filepath.Join(root, "Dune (2021).mkv")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "color=c=black:s=64x36:r=1:d=3660", "-c:v", "libx264", "-preset", "ultrafast", "-g", "600",
		"-pix_fmt", "yuv420p", movie).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}

	tvmaze := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "[]")
	}))
	t.Cleanup(tvmaze.Close)
	proxy := newProbeProxy(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	config := fmt.Sprintf(`server:
  bind: "127.0.0.1"
  port: %d
data_dir: %q
hls_cache:
  dir: %q
  max_gb: 1
library_roots:
  - name: "Media"
    path: %q
trash:
  retention_days: 36500
tvmaze:
  base_url: %q
%s
log:
  level: "debug"
`, port, dataDir, filepath.Join(dataDir, "hls"), root, tvmaze.URL, tokenYAML)
	configPath := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	output := &bytes.Buffer{}
	cmd := exec.Command(bin, append([]string{"--config", configPath}, extraArgs...)...)
	cmd.Env = append(os.Environ(),
		"HTTPS_PROXY=http://"+proxy.ln.Addr().String(),
		"HTTP_PROXY=http://"+proxy.ln.Addr().String(),
		"https_proxy=http://"+proxy.ln.Addr().String(),
		"http_proxy=http://"+proxy.ln.Addr().String(),
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})

	run := &probeRun{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), dataDir: dataDir, output: output, proxy: proxy}
	run.waitFor("the server to answer", 20*time.Second, func() bool {
		resp, err := http.Get(run.base + "/api/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return run
}

func (r *probeRun) waitFor(what string, limit time.Duration, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(limit)
	for !ok() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s; server output:\n%s", what, r.output.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (r *probeRun) call(method, path, body string) (int, string) {
	r.t.Helper()
	req, err := http.NewRequest(method, r.base+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r.bodies = append(r.bodies, fmt.Sprintf("%s %s → %d %s", method, path, resp.StatusCode, b))
	return resp.StatusCode, string(b)
}

// movieID waits until the probe has filed the lavfi file as a movie and its
// classify job has finished.
func (r *probeRun) movieID() int64 {
	r.t.Helper()
	var id int64
	r.waitFor("the movie to be classified", 45*time.Second, func() bool {
		_, body := r.call("GET", "/api/items?limit=50", "")
		var list struct {
			Items []struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(body), &list) != nil {
			return false
		}
		for _, it := range list.Items {
			if it.Type == "movie" {
				id = it.ID
			}
		}
		if id == 0 {
			return false
		}
		_, jobs := r.call("GET", "/api/jobs?limit=200", "")
		var js []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(jobs), &js) != nil {
			return false
		}
		seen := false
		for _, j := range js {
			if j.Type == "classify" {
				seen = true
				if j.Status == "queued" || j.Status == "running" {
					return false
				}
			}
		}
		return seen
	})
	return id
}

func (r *probeRun) exercise(id int64) {
	r.t.Helper()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/health", ""},
		{"GET", "/api/settings", ""},
		{"GET", "/api/items?limit=50", ""},
		{"GET", fmt.Sprintf("/api/items/%d", id), ""},
		{"GET", "/api/library", ""},
		{"GET", "/api/jobs?limit=200", ""},
		{"GET", fmt.Sprintf("/api/items/%d/match-search?q=dune&year=2021", id), ""},
		{"POST", fmt.Sprintf("/api/items/%d/match", id), `{"tmdb_id": 438631}`},
		{"POST", fmt.Sprintf("/api/items/%d/match", id), `{"tmdb_id": null}`},
		{"GET", fmt.Sprintf("/api/items/%d/art", id), ""},
	} {
		r.call(c.method, c.path, c.body)
	}
}

// assertHidden searches every response, the server's output, the proxy's
// view of each request and every byte under data_dir for the token.
func (r *probeRun) assertHidden(token string) {
	r.t.Helper()
	for _, b := range r.bodies {
		if strings.Contains(b, token) {
			r.t.Errorf("token in API response: %.200s", b)
		}
	}
	if strings.Contains(r.output.String(), token) {
		r.t.Error("token in server stdout/stderr")
	}
	if strings.Contains(r.proxy.all(), token) {
		r.t.Error("token visible to the network outside TLS")
	}
	filepath.WalkDir(r.dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, []byte(token)) {
			r.t.Errorf("token in data dir file %s", path)
		}
		return nil
	})
}

// probe: with a token set, the real server reaches for TMDB yet the token appears in no response, log line, outbound plaintext or data_dir file
func TestProbe_Token_RealServerKeepsTokenHidden(t *testing.T) {
	run := startProbeServer(t, fmt.Sprintf("tmdb:\n  read_access_token: %q", probeToken))
	id := run.movieID()
	run.waitFor("a TMDB request at the proxy", 20*time.Second, func() bool { return len(run.proxy.tmdbRequests()) > 0 })
	run.exercise(id)
	time.Sleep(time.Second)
	run.assertHidden(probeToken)
}

// probe: --tmdb-token-from keeps the token as hidden as config does
func TestProbe_Token_TokenFromFlagKeepsTokenHidden(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "owner-config.yml")
	if err := os.WriteFile(tokenFile, []byte(fmt.Sprintf("tmdb:\n  read_access_token: %q\n", probeToken)), 0o600); err != nil {
		t.Fatal(err)
	}
	run := startProbeServer(t, "tmdb:\n  read_access_token: \"\"", "--tmdb-token-from", tokenFile)
	id := run.movieID()
	run.waitFor("a TMDB request at the proxy", 20*time.Second, func() bool { return len(run.proxy.tmdbRequests()) > 0 })
	run.exercise(id)
	time.Sleep(time.Second)
	run.assertHidden(probeToken)
}

// probe: an empty or blank token makes zero TMDB requests through boot, classify and every movie endpoint
func TestProbe_Token_TokenlessServerMakesNoTMDBRequest(t *testing.T) {
	for name, yaml := range map[string]string{
		"empty":  "tmdb:\n  read_access_token: \"\"",
		"blank":  "tmdb:\n  read_access_token: \"   \"",
		"absent": "",
	} {
		t.Run(name, func(t *testing.T) {
			run := startProbeServer(t, yaml)
			id := run.movieID()
			run.exercise(id)
			time.Sleep(2 * time.Second)
			if reqs := run.proxy.tmdbRequests(); len(reqs) > 0 {
				t.Errorf("tokenless server made TMDB requests: %v", reqs)
			}
		})
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if probeBin != "" {
		os.RemoveAll(filepath.Dir(probeBin))
	}
	os.Exit(code)
}
