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
	"sync/atomic"
	"time"
)

var (
	ErrNotFound         = errors.New("playback session not found")
	ErrTimeout          = errors.New("playback segment timed out")
	ErrCacheUnavailable = errors.New("hls cache dir unavailable")
)

type Manager struct {
	cacheDir string
	maxBytes int64
	ffmpeg   string
	ffprobe  string
	log      *slog.Logger

	segmentDuration time.Duration
	segmentWait     time.Duration
	pollInterval    time.Duration
	idleTimeout     time.Duration
	sessionTimeout  time.Duration

	videoSem chan struct{}

	mu       sync.Mutex
	sessions map[string]*Session
	workers  map[string]*worker

	// procs tracks the ffmpeg reaping goroutines so Shutdown can wait for
	// every child process to actually exit before the server does.
	procs sync.WaitGroup

	// cacheDirty is set whenever a worker starts or stops writing; the prune
	// tick only walks the cache while something is running or right after.
	cacheDirty atomic.Bool
}

type Options struct {
	CacheDir        string
	MaxBytes        int64
	FFmpeg          string
	FFprobe         string
	MaxVideoWorkers int
	Log             *slog.Logger

	SegmentDuration time.Duration
	SegmentWait     time.Duration
	PollInterval    time.Duration
	IdleTimeout     time.Duration
	SessionTimeout  time.Duration
}

type StartRequest struct {
	File                MediaFile
	SourcePath          string
	Streams             []Stream
	Capabilities        Capabilities
	Decision            Decision
	SubtitleStreamIndex *int
	AudioStreamIndex    *int
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
	if opts.SessionTimeout <= 0 {
		opts.SessionTimeout = 6 * time.Hour
	}
	return &Manager{
		cacheDir:        opts.CacheDir,
		maxBytes:        opts.MaxBytes,
		ffmpeg:          opts.FFmpeg,
		ffprobe:         opts.FFprobe,
		log:             opts.Log,
		segmentDuration: opts.SegmentDuration,
		segmentWait:     opts.SegmentWait,
		pollInterval:    opts.PollInterval,
		idleTimeout:     opts.IdleTimeout,
		sessionTimeout:  opts.SessionTimeout,
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
				if !m.cacheDirty.Swap(false) && !m.anyRunning() {
					continue
				}
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
	cacheDir := m.CacheDir()
	if cacheDir == "" {
		return Session{}, fmt.Errorf("hls cache dir is required")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrCacheUnavailable, err)
	}
	hash := ProfileHash(req.File, req.Capabilities, req.Decision, req.SubtitleStreamIndex, req.AudioStreamIndex)
	key := strconv.FormatInt(req.File.ID, 10) + "/" + hash
	dir := filepath.Join(cacheDir, strconv.FormatInt(req.File.ID, 10), hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrCacheUnavailable, err)
	}

	var keyframes *keyframeIndex
	if req.Decision.Tier == TierRemux || req.Decision.Tier == TierAudioTranscode {
		idx, err := loadOrProbeKeyframes(ctx, cacheDir, m.ffprobe, req.SourcePath, req.File)
		if err != nil {
			return Session{}, fmt.Errorf("index playback keyframes: %w", err)
		}
		keyframes = &idx
	}

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
			markWrite:       m.markCacheWrite,
			keyframes:       keyframes,
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

// RungPlaylist is one ladder variant's media playlist. Non-ladder sessions and
// rungs this session does not encode are ErrNotFound.
func (m *Manager) RungPlaylist(ctx context.Context, sid, rung string) (string, error) {
	w, err := m.workerForSession(sid)
	if err != nil {
		return "", err
	}
	w.touch()
	return w.rungPlaylist(rung)
}

func (m *Manager) ServeRungSegment(wr http.ResponseWriter, r *http.Request, sid, rung, name string) error {
	w, err := m.workerForSession(sid)
	if err != nil {
		return err
	}
	return w.serveSegment(wr, r, rung, name)
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
	cacheDir := m.CacheDir()
	if m.maxBytes <= 0 || cacheDir == "" {
		return nil
	}
	entries, total, err := cacheEntries(cacheDir)
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

func (m *Manager) markCacheWrite() { m.cacheDirty.Store(true) }

func (m *Manager) anyRunning() bool {
	m.mu.Lock()
	workers := make([]*worker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
	}
	m.mu.Unlock()
	for _, w := range workers {
		w.mu.Lock()
		running := w.running
		w.mu.Unlock()
		if running {
			return true
		}
	}
	return false
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

func (m *Manager) CacheDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cacheDir
}

func (m *Manager) CacheLimit() int64 { return m.maxBytes }

// CacheUsage totals the segment and index files under the cache dir. A
// missing directory (unmounted volume) counts as empty.
func (m *Manager) CacheUsage() (int64, error) {
	_, total, err := cacheEntries(m.CacheDir())
	return total, err
}

// SetCacheDir points new sessions at dir. Every current session is stopped:
// its worker writes into the old tree, which the caller is expected to clear
// with ClearCache once this returns. Returns the previous directory.
func (m *Manager) SetCacheDir(dir string) string {
	m.StopAll()
	m.mu.Lock()
	old := m.cacheDir
	m.cacheDir = dir
	m.mu.Unlock()
	return old
}

// ClearCache deletes the per-file trees the manager created under dir and
// nothing else, so a misconfigured path can never take unrelated data with
// it. Entries still in use by a running worker are skipped.
func (m *Manager) ClearCache(dir string) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	active := m.activeCacheDirs()
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.ParseInt(entry.Name(), 10, 64); err != nil {
			continue
		}
		fileDir := filepath.Join(dir, entry.Name())
		inUse := false
		for activeDir := range active {
			if strings.HasPrefix(activeDir, fileDir+string(filepath.Separator)) {
				inUse = true
				break
			}
		}
		if inUse {
			continue
		}
		if err := os.RemoveAll(fileDir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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

// reapIdle enforces two tiers of idleness. After idleTimeout the ffmpeg child
// is killed but the session, worker, and cached segments all survive — clients
// that buffer minutes ahead (Safari native HLS) legitimately go quiet longer
// than the timeout and must be able to come back and resume; the transcode
// restarts on demand at whatever segment they ask for next. Only after
// sessionTimeout (a browser that vanished without its pagehide beacon) is the
// session mapping dropped.
func (m *Manager) reapIdle(now time.Time) {
	m.mu.Lock()
	var stop []*worker
	for key, w := range m.workers {
		switch {
		case w.idleSince(now, m.sessionTimeout):
			for sid, session := range m.sessions {
				if session.WorkerKey == key {
					delete(m.sessions, sid)
				}
			}
			delete(m.workers, key)
			stop = append(stop, w)
		case w.idleSince(now, m.idleTimeout):
			stop = append(stop, w)
		}
	}
	m.mu.Unlock()
	for _, w := range stop {
		w.stop()
	}
}

type worker struct {
	key string
	dir string
	req StartRequest

	ffmpeg    string
	log       *slog.Logger
	videoSem  chan struct{}
	procs     *sync.WaitGroup
	markWrite func()
	keyframes *keyframeIndex

	segmentDuration time.Duration
	segmentWait     time.Duration
	pollInterval    time.Duration

	mu          sync.Mutex
	cmdCancel   context.CancelFunc
	cmdToken    *struct{}
	running     bool
	startNumber int
	lastAccess  time.Time
	semHeld     bool
	stderr      *tailBuffer
}

func (w *worker) noteWrite() {
	if w.markWrite != nil {
		w.markWrite()
	}
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
	switch {
	case w.isLadder():
		return MasterPlaylist(w.req.Decision.Rungs, w.ladderHEVC())
	case w.keyframes != nil:
		return w.keyframePlaylist()
	}
	return w.gridPlaylist()
}

func (w *worker) rungPlaylist(rung string) (string, error) {
	if _, err := w.outputDir(rung); err != nil {
		return "", err
	}
	return w.gridPlaylist(), nil
}

func (w *worker) isLadder() bool {
	return w.req.Decision.Tier == TierLadder
}

func (w *worker) ladderHEVC() bool {
	_, hevc := ladderVideoCodec(w.req.Capabilities)
	return hevc
}

// outputDir is the directory a request's segments live in: the profile dir for
// a flat session, one subdirectory per rung for a ladder.
func (w *worker) outputDir(rung string) (string, error) {
	if !w.isLadder() {
		if rung != "" {
			return "", ErrNotFound
		}
		return w.dir, nil
	}
	for _, r := range w.req.Decision.Rungs {
		if r.ID == rung {
			return filepath.Join(w.dir, rung), nil
		}
	}
	return "", ErrNotFound
}

func (w *worker) gridPlaylist() string {
	duration := w.req.File.DurationS
	if duration <= 0 {
		duration = w.segmentDuration.Seconds()
	}
	count := w.segmentCount()
	var b strings.Builder
	writeMediaPlaylistHeader(&b, int(math.Ceil(w.segmentDuration.Seconds())))
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

func (w *worker) keyframePlaylist() string {
	var b strings.Builder
	writeMediaPlaylistHeader(&b, w.keyframes.targetDuration())
	for n := range w.keyframes.Starts {
		b.WriteString("#EXTINF:" + strconv.FormatFloat(w.keyframes.segmentDuration(n), 'f', 6, 64) + ",\n")
		b.WriteString(segmentName(n) + "\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func writeMediaPlaylistHeader(b *strings.Builder, targetDuration int) {
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-TARGETDURATION:" + strconv.Itoa(targetDuration) + "\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
}

func (w *worker) serveSegment(wr http.ResponseWriter, r *http.Request, rung, name string) error {
	w.touch()
	dir, err := w.outputDir(rung)
	if err != nil {
		return err
	}
	if name == "init.mp4" {
		path := filepath.Join(dir, name)
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
	path := filepath.Join(dir, segmentName(n))
	if _, err := os.Stat(path); err != nil {
		if err := w.ensureSegment(r.Context(), n, dir); err != nil {
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

// ensureSegment measures progress against dir. A ladder's rungs advance in
// lockstep, so the requested rung's newest segment speaks for all of them.
func (w *worker) ensureSegment(ctx context.Context, n int, dir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running && n >= w.startNumber && n <= w.diskHighLocked(dir)+w.forwardWindow() {
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
	for _, rung := range w.req.Decision.Rungs {
		if err := os.MkdirAll(filepath.Join(w.dir, rung.ID), 0o755); err != nil {
			return err
		}
	}

	startNumber, seekS := n, float64(n)*w.segmentDuration.Seconds()
	startTime := seekS
	timestampOffset := 0.0
	if w.keyframes != nil {
		seekS = 0
		startTime = w.keyframes.Starts[n] - w.keyframes.Starts[0]
		if n > 0 {
			// Input -ss has a small B-frame preroll. Asking just after the indexed
			// random-access point makes demuxer seeking land on that point instead
			// of falling back one whole GOP.
			seekS = w.keyframes.Starts[n] + 0.2
			timestampOffset = w.keyframes.TimestampShift
		}
	}

	semHeld := false
	if videoEncodingTier(w.req.Decision.Tier) {
		select {
		case w.videoSem <- struct{}{}:
			semHeld = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	procCtx, cancel := context.WithTimeout(context.Background(), processTimeout(w.req.File.DurationS, startTime))
	cmd := ffmpegCommand(procCtx, FFmpegRequest{
		Binary:          w.ffmpeg,
		SourcePath:      w.req.SourcePath,
		OutputDir:       w.dir,
		Decision:        w.req.Decision,
		Capabilities:    w.req.Capabilities,
		File:            w.req.File,
		Streams:         w.req.Streams,
		StartSegment:    startNumber,
		SeekSeconds:     seekS,
		TimestampOffset: timestampOffset,
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
	w.startNumber = startNumber
	w.semHeld = semHeld
	w.stderr = stderr
	w.noteWrite()

	if w.procs != nil {
		w.procs.Add(1)
	}
	go func() {
		if w.procs != nil {
			defer w.procs.Done()
		}
		err := cmd.Wait()
		w.noteWrite()
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

// videoEncodingTier gates transcode.max_concurrent. A ladder holds one slot
// however many rungs its single ffmpeg encodes.
func videoEncodingTier(tier string) bool {
	return tier == TierFullTranscode || tier == TierLadder
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

// diskHighLocked reports the highest segment number present in the cache dir —
// the ground truth of transcode progress. The old wall-clock estimate assumed
// exactly 1× realtime, so a fast remux looked "behind" and a prefetching
// client's miss would kill and restart a healthy ffmpeg mid-stream.
func (w *worker) diskHighLocked(dir string) int {
	high := w.startNumber - 1
	entries, err := os.ReadDir(dir)
	if err != nil {
		return high
	}
	for _, entry := range entries {
		if n, ok := parseSegmentName(entry.Name()); ok && n > high {
			high = n
		}
	}
	return high
}

// forwardWindow is how far past ffmpeg's newest segment a request may wait
// rather than trigger a seek-restart — sized so a ≥1× transcode can plausibly
// produce the segment within one segmentWait.
func (w *worker) forwardWindow() int {
	window := int(w.segmentWait / w.segmentDuration)
	if window < 4 {
		window = 4
	}
	return window
}

// tailSlopS absorbs the gap between the probed container duration and where
// the streams actually end (audio priming, container rounding). Without it a
// file probed at 60.023 s advertises a 16th segment ffmpeg never writes, and
// the player hangs on a 504 at the end of the video. A sub-half-second sliver
// folds into the previous segment's EXTINF instead.
const tailSlopS = 0.5

func (w *worker) segmentCount() int {
	if w.keyframes != nil {
		return len(w.keyframes.Starts)
	}
	duration := w.req.File.DurationS
	if duration <= 0 {
		return 1
	}
	count := int(math.Ceil((duration - tailSlopS) / w.segmentDuration.Seconds()))
	if count < 1 {
		return 1
	}
	return count
}

func processTimeout(durationS, startTime float64) time.Duration {
	remaining := durationS - startTime
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
