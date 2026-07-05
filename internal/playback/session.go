package playback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("playback session not found")
	ErrTimeout  = errors.New("playback segment timed out")
)

type Manager struct {
	cacheDir string
	maxBytes int64
	ffmpeg   string
	log      *slog.Logger

	segmentDuration time.Duration
	segmentWait     time.Duration
	pollInterval    time.Duration
	idleTimeout     time.Duration

	videoSem chan struct{}

	mu       sync.Mutex
	sessions map[string]*Session
	workers  map[string]*worker

	// procs tracks the ffmpeg reaping goroutines so Shutdown can wait for
	// every child process to actually exit before the server does.
	procs sync.WaitGroup
}

type Options struct {
	CacheDir        string
	MaxBytes        int64
	FFmpeg          string
	MaxVideoWorkers int
	Log             *slog.Logger

	SegmentDuration time.Duration
	SegmentWait     time.Duration
	PollInterval    time.Duration
	IdleTimeout     time.Duration
}

type StartRequest struct {
	File                MediaFile
	SourcePath          string
	Streams             []Stream
	Capabilities        Capabilities
	Decision            Decision
	SubtitleStreamIndex *int
}

type Session struct {
	ID        string
	URL       string
	WorkerKey string
}

func NewManager(opts Options) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.MaxVideoWorkers <= 0 {
		opts.MaxVideoWorkers = 2
	}
	if opts.SegmentDuration <= 0 {
		opts.SegmentDuration = DefaultSegmentDuration
	}
	if opts.SegmentWait <= 0 {
		opts.SegmentWait = 20 * time.Second
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 200 * time.Millisecond
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 60 * time.Second
	}
	return &Manager{
		cacheDir:        opts.CacheDir,
		maxBytes:        opts.MaxBytes,
		ffmpeg:          opts.FFmpeg,
		log:             opts.Log,
		segmentDuration: opts.SegmentDuration,
		segmentWait:     opts.SegmentWait,
		pollInterval:    opts.PollInterval,
		idleTimeout:     opts.IdleTimeout,
		videoSem:        make(chan struct{}, opts.MaxVideoWorkers),
		sessions:        make(map[string]*Session),
		workers:         make(map[string]*worker),
	}
}

func (m *Manager) Start(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				m.StopAll()
				return
			case <-ticker.C:
				m.reapIdle(time.Now())
				if err := m.PruneCache(ctx); err != nil && ctx.Err() == nil {
					m.log.Debug("prune hls cache", "error", err)
				}
			}
		}
	}()
}

func (m *Manager) StartSession(ctx context.Context, req StartRequest) (Session, error) {
	if req.Decision.Mode != ModeHLS {
		return Session{}, fmt.Errorf("cannot start hls session for mode %q", req.Decision.Mode)
	}
	if m.cacheDir == "" {
		return Session{}, fmt.Errorf("hls cache dir is required")
	}
	if err := os.MkdirAll(m.cacheDir, 0o755); err != nil {
		return Session{}, fmt.Errorf("create hls cache: %w", err)
	}
	hash := ProfileHash(req.File, req.Capabilities, req.Decision, req.SubtitleStreamIndex)
	key := strconv.FormatInt(req.File.ID, 10) + "/" + hash
	dir := filepath.Join(m.cacheDir, strconv.FormatInt(req.File.ID, 10), hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Session{}, fmt.Errorf("create hls session cache: %w", err)
	}
	_ = m.PruneCache(ctx)

	sid, err := randomID()
	if err != nil {
		return Session{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[key]
	if w == nil {
		w = &worker{
			key:             key,
			dir:             dir,
			req:             req,
			ffmpeg:          m.ffmpeg,
			log:             m.log,
			videoSem:        m.videoSem,
			procs:           &m.procs,
			segmentDuration: m.segmentDuration,
			segmentWait:     m.segmentWait,
			pollInterval:    m.pollInterval,
			lastAccess:      time.Now(),
		}
		m.workers[key] = w
	}
	session := &Session{ID: sid, URL: "/api/sessions/" + sid + "/master.m3u8", WorkerKey: key}
	m.sessions[sid] = session
	return *session, nil
}

func (m *Manager) ActiveSessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *Manager) Playlist(ctx context.Context, sid string) (string, error) {
	w, err := m.workerForSession(sid)
	if err != nil {
		return "", err
	}
	w.touch()
	return w.playlist(), nil
}

func (m *Manager) ServeSegment(wr http.ResponseWriter, r *http.Request, sid, name string) error {
	w, err := m.workerForSession(sid)
	if err != nil {
		return err
	}
	return w.serveSegment(wr, r, name)
}

func (m *Manager) EndSession(sid string) bool {
	m.mu.Lock()
	session := m.sessions[sid]
	if session == nil {
		m.mu.Unlock()
		return false
	}
	delete(m.sessions, sid)
	worker := m.workers[session.WorkerKey]
	hasRefs := false
	for _, other := range m.sessions {
		if other.WorkerKey == session.WorkerKey {
			hasRefs = true
			break
		}
	}
	if !hasRefs {
		delete(m.workers, session.WorkerKey)
	}
	m.mu.Unlock()

	if worker != nil && !hasRefs {
		worker.stop()
	}
	return true
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	workers := make([]*worker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
	}
	m.sessions = make(map[string]*Session)
	m.workers = make(map[string]*worker)
	m.mu.Unlock()

	for _, w := range workers {
		w.stop()
	}
}

// Shutdown cancels every running transcode, killing its ffmpeg child, and
// waits (bounded by ctx) for those processes to be reaped so none are left
// orphaned after the server exits. Call it only after the HTTP server has
// stopped accepting requests, so no new session races the teardown.
func (m *Manager) Shutdown(ctx context.Context) {
	m.StopAll()
	done := make(chan struct{})
	go func() {
		m.procs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		m.log.Warn("playback shutdown timed out waiting for ffmpeg children to exit")
	}
}

func (m *Manager) PruneCache(ctx context.Context) error {
	if m.maxBytes <= 0 || m.cacheDir == "" {
		return nil
	}
	entries, total, err := cacheEntries(m.cacheDir)
	if err != nil {
		return err
	}
	if total <= m.maxBytes {
		return nil
	}

	active := m.activeCacheDirs()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mod.Before(entries[j].mod)
	})
	for _, entry := range entries {
		if total <= m.maxBytes || ctx.Err() != nil {
			break
		}
		if active[entry.path] {
			continue
		}
		if err := os.RemoveAll(entry.path); err != nil {
			return err
		}
		total -= entry.size
	}
	return ctx.Err()
}

func (m *Manager) workerForSession(sid string) (*worker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sid]
	if session == nil {
		return nil, ErrNotFound
	}
	w := m.workers[session.WorkerKey]
	if w == nil {
		return nil, ErrNotFound
	}
	return w, nil
}

func (m *Manager) activeCacheDirs() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := make(map[string]bool, len(m.workers))
	for _, w := range m.workers {
		active[w.dir] = true
	}
	return active
}

func (m *Manager) reapIdle(now time.Time) {
	m.mu.Lock()
	type stopped struct {
		key string
		w   *worker
	}
	var stop []stopped
	for key, w := range m.workers {
		if !w.idleSince(now, m.idleTimeout) {
			continue
		}
		stop = append(stop, stopped{key: key, w: w})
		for sid, session := range m.sessions {
			if session.WorkerKey == key {
				delete(m.sessions, sid)
			}
		}
		delete(m.workers, key)
	}
	m.mu.Unlock()
	for _, item := range stop {
		item.w.stop()
	}
}

type worker struct {
	key string
	dir string
	req StartRequest

	ffmpeg   string
	log      *slog.Logger
	videoSem chan struct{}
	procs    *sync.WaitGroup

	segmentDuration time.Duration
	segmentWait     time.Duration
	pollInterval    time.Duration

	mu          sync.Mutex
	cmdCancel   context.CancelFunc
	cmdToken    *struct{}
	running     bool
	startNumber int
	startedAt   time.Time
	lastAccess  time.Time
	semHeld     bool
	stderr      *tailBuffer
}

func (w *worker) touch() {
	w.mu.Lock()
	w.lastAccess = time.Now()
	w.mu.Unlock()
}

func (w *worker) idleSince(now time.Time, timeout time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.lastAccess.IsZero() && now.Sub(w.lastAccess) > timeout
}

func (w *worker) playlist() string {
	duration := w.req.File.DurationS
	if duration <= 0 {
		duration = w.segmentDuration.Seconds()
	}
	count := int(math.Ceil(duration / w.segmentDuration.Seconds()))
	if count < 1 {
		count = 1
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-TARGETDURATION:" + strconv.Itoa(int(math.Ceil(w.segmentDuration.Seconds()))) + "\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i := 0; i < count; i++ {
		remaining := duration - float64(i)*w.segmentDuration.Seconds()
		segDuration := math.Min(w.segmentDuration.Seconds(), remaining)
		if segDuration <= 0 {
			segDuration = w.segmentDuration.Seconds()
		}
		b.WriteString("#EXTINF:" + strconv.FormatFloat(segDuration, 'f', 3, 64) + ",\n")
		b.WriteString(segmentName(i) + "\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func (w *worker) serveSegment(wr http.ResponseWriter, r *http.Request, name string) error {
	w.touch()
	if name == "init.mp4" {
		path := filepath.Join(w.dir, name)
		if _, err := os.Stat(path); err != nil {
			if err := w.ensureRunning(r.Context(), 0); err != nil {
				return err
			}
			if err := waitForFile(r.Context(), path, w.segmentWait, w.pollInterval); err != nil {
				return err
			}
		}
		wr.Header().Set("Content-Type", "video/mp4")
		http.ServeFile(wr, r, path)
		return nil
	}

	n, ok := parseSegmentName(name)
	if !ok || n >= w.segmentCount() {
		return ErrNotFound
	}
	path := filepath.Join(w.dir, segmentName(n))
	if _, err := os.Stat(path); err != nil {
		if err := w.ensureSegment(r.Context(), n); err != nil {
			return err
		}
		if err := waitForFile(r.Context(), path, w.segmentWait, w.pollInterval); err != nil {
			return err
		}
	}
	wr.Header().Set("Content-Type", "video/iso.segment")
	http.ServeFile(wr, r, path)
	return nil
}

func (w *worker) ensureSegment(ctx context.Context, n int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running && n >= w.startNumber && n <= w.forwardHighLocked(time.Now()) {
		return nil
	}
	return w.startLocked(ctx, n)
}

func (w *worker) ensureRunning(ctx context.Context, n int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return nil
	}
	return w.startLocked(ctx, n)
}

func (w *worker) startLocked(ctx context.Context, n int) error {
	w.stopLocked()
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}

	semHeld := false
	if w.req.Decision.Tier == TierFullTranscode {
		select {
		case w.videoSem <- struct{}{}:
			semHeld = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	procCtx, cancel := context.WithTimeout(context.Background(), processTimeout(w.req.File.DurationS, n, w.segmentDuration))
	cmd := ffmpegCommand(procCtx, FFmpegRequest{
		Binary:          w.ffmpeg,
		SourcePath:      w.req.SourcePath,
		OutputDir:       w.dir,
		Decision:        w.req.Decision,
		Capabilities:    w.req.Capabilities,
		File:            w.req.File,
		Streams:         w.req.Streams,
		StartSegment:    n,
		SegmentDuration: w.segmentDuration,
	}, w.log)
	stderr := newTailBuffer(32 * 1024)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		if semHeld {
			<-w.videoSem
		}
		return err
	}
	w.cmdCancel = cancel
	token := &struct{}{}
	w.cmdToken = token
	w.running = true
	w.startNumber = n
	w.startedAt = time.Now()
	w.semHeld = semHeld
	w.stderr = stderr

	if w.procs != nil {
		w.procs.Add(1)
	}
	go func() {
		if w.procs != nil {
			defer w.procs.Done()
		}
		err := cmd.Wait()
		if semHeld {
			<-w.videoSem
		}
		w.mu.Lock()
		if w.cmdToken == token {
			w.cmdCancel = nil
			w.cmdToken = nil
			w.running = false
			w.semHeld = false
		}
		w.mu.Unlock()
		cancel()
		if err != nil && w.log != nil {
			w.log.Debug("ffmpeg hls exited", "profile", w.key, "error", err, "stderr", stderr.String())
		}
	}()
	return nil
}

func (w *worker) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
}

func (w *worker) stopLocked() {
	if w.cmdCancel != nil {
		w.cmdCancel()
		w.cmdCancel = nil
		w.cmdToken = nil
	}
	w.running = false
}

func (w *worker) forwardHighLocked(now time.Time) int {
	elapsedSegments := int(now.Sub(w.startedAt) / w.segmentDuration)
	if elapsedSegments < 0 {
		elapsedSegments = 0
	}
	return w.startNumber + elapsedSegments + 4
}

func (w *worker) segmentCount() int {
	duration := w.req.File.DurationS
	if duration <= 0 {
		return 1
	}
	count := int(math.Ceil(duration / w.segmentDuration.Seconds()))
	if count < 1 {
		return 1
	}
	return count
}

func processTimeout(durationS float64, startSegment int, segmentDuration time.Duration) time.Duration {
	remaining := durationS - float64(startSegment)*segmentDuration.Seconds()
	if remaining <= 0 {
		remaining = durationS
	}
	if remaining <= 0 {
		return 12 * time.Hour
	}
	timeout := time.Duration(remaining*4)*time.Second + 30*time.Minute
	if timeout < 30*time.Minute {
		return 30 * time.Minute
	}
	if timeout > 12*time.Hour {
		return 12 * time.Hour
	}
	return timeout
}

func waitForFile(ctx context.Context, path string, timeout, poll time.Duration) error {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrTimeout
		case <-ticker.C:
		}
	}
}

func segmentName(n int) string {
	return fmt.Sprintf("seg-%05d.m4s", n)
}

func parseSegmentName(name string) (int, bool) {
	if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".m4s") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".m4s")
	n, err := strconv.Atoi(raw)
	return n, err == nil && n >= 0
}

func randomID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

type cacheEntry struct {
	path string
	size int64
	mod  time.Time
}

func cacheEntries(cacheDir string) ([]cacheEntry, int64, error) {
	rootEntries, err := os.ReadDir(cacheDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	var entries []cacheEntry
	var total int64
	for _, fileDir := range rootEntries {
		if !fileDir.IsDir() {
			continue
		}
		profileRoot := filepath.Join(cacheDir, fileDir.Name())
		profiles, err := os.ReadDir(profileRoot)
		if err != nil {
			continue
		}
		for _, profile := range profiles {
			if !profile.IsDir() {
				continue
			}
			entry := cacheEntry{path: filepath.Join(profileRoot, profile.Name())}
			err := filepath.WalkDir(entry.path, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				entry.size += info.Size()
				if info.ModTime().After(entry.mod) {
					entry.mod = info.ModTime()
				}
				return nil
			})
			if err != nil {
				return nil, 0, err
			}
			entries = append(entries, entry)
			total += entry.size
		}
	}
	return entries, total, nil
}
