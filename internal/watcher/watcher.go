// Package watcher turns filesystem and mount changes into reconcile/probe
// jobs and availability events. SQL stays in store; media ingest stays in jobs.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
)

const (
	defaultStreamLatency      = 500 * time.Millisecond
	defaultDebounce           = 500 * time.Millisecond
	defaultQuiesceInterval    = 2 * time.Second
	defaultQuiesceStableFor   = 5 * time.Second
	defaultMoveWindow         = 10 * time.Second
	defaultMountCheckInterval = 30 * time.Second
	defaultStormBackstop      = 3 * time.Second
	maxQuiesceAge             = 24 * time.Hour
)

type Enqueuer interface {
	EnqueueReconcile(ctx context.Context, rootID int64) (store.Job, error)
	EnqueueProbe(ctx context.Context, rootID int64, relPath string) (store.Job, error)
}

type Manager struct {
	store *store.Store
	jobs  Enqueuer
	bus   *events.Bus
	log   *slog.Logger

	ignore *IgnoreSet

	streamLatency      time.Duration
	debounce           time.Duration
	quiesceInterval    time.Duration
	quiesceStableFor   time.Duration
	moveWindow         time.Duration
	mountCheckInterval time.Duration
	stormBackstop      time.Duration
	streamFactory      streamFactory

	mu        sync.Mutex
	runners   map[int64]context.CancelFunc
	parentCtx context.Context
	wg        sync.WaitGroup

	// syncMu serializes syncRoots: the mount ticker and the /Volumes stream
	// both call it, and interleaved runs could double-publish transitions or
	// double-enqueue reconciles for the same root.
	syncMu sync.Mutex
}

type Options struct {
	Store *store.Store
	Jobs  Enqueuer
	Bus   *events.Bus
	Log   *slog.Logger

	Ignore *IgnoreSet

	StreamLatency      time.Duration
	Debounce           time.Duration
	QuiesceInterval    time.Duration
	QuiesceStableFor   time.Duration
	MoveWindow         time.Duration
	MountCheckInterval time.Duration
	StormBackstop      time.Duration

	StreamFactory streamFactory
}

func NewManager(opts Options) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Ignore == nil {
		opts.Ignore = NewIgnoreSet()
	}
	return &Manager{
		store:              opts.Store,
		jobs:               opts.Jobs,
		bus:                opts.Bus,
		log:                opts.Log,
		ignore:             opts.Ignore,
		streamLatency:      withDefault(opts.StreamLatency, defaultStreamLatency),
		debounce:           withDefault(opts.Debounce, defaultDebounce),
		quiesceInterval:    withDefault(opts.QuiesceInterval, defaultQuiesceInterval),
		quiesceStableFor:   withDefault(opts.QuiesceStableFor, defaultQuiesceStableFor),
		moveWindow:         withDefault(opts.MoveWindow, defaultMoveWindow),
		mountCheckInterval: withDefault(opts.MountCheckInterval, defaultMountCheckInterval),
		stormBackstop:      withDefault(opts.StormBackstop, defaultStormBackstop),
		streamFactory:      firstStreamFactory(opts.StreamFactory),
		runners:            make(map[int64]context.CancelFunc),
	}
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.parentCtx = ctx
	m.mu.Unlock()
	if err := m.syncRoots(ctx, true); err != nil {
		return err
	}
	m.wg.Add(2)
	go func() { defer m.wg.Done(); m.mountLoop(ctx) }()
	go func() { defer m.wg.Done(); m.volumesLoop(ctx) }()
	return nil
}

// Wait blocks until the mount/volumes loops and every per-root watch
// goroutine have exited (they stop when the context passed to Start is
// cancelled). Call after cancelling that context and before closing the DB.
func (m *Manager) Wait() {
	m.wg.Wait()
}

// AttachRoot starts watching a newly attached root immediately and enqueues
// its reconcile without waiting for the periodic mount scan.
func (m *Manager) AttachRoot(ctx context.Context, root store.Root) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	if !dirExists(root.Path) {
		m.stopRoot(root.ID)
		return m.setRootOnline(ctx, root, false)
	}
	if err := m.setRootOnline(ctx, root, true); err != nil {
		return err
	}
	root.Online = true
	m.ensureRootWatcher(m.backgroundContext(), root)
	m.enqueueReconcile(ctx, root.ID)
	return nil
}

// DetachRoot stops watching a root and marks its files offline while keeping
// the catalog rows for future reattachment of the same path.
func (m *Manager) DetachRoot(ctx context.Context, root store.Root) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	m.stopRoot(root.ID)
	if err := m.store.DetachRoot(ctx, root.ID); err != nil {
		return err
	}
	m.publish(events.Event{
		Type:    events.RootStatus,
		Payload: map[string]any{"id": root.ID, "online": false},
	})
	m.log.Warn("library root detached", "root_id", root.ID, "name", root.Name, "path", root.Path)
	return nil
}

func (m *Manager) backgroundContext() context.Context {
	m.mu.Lock()
	ctx := m.parentCtx
	m.mu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (m *Manager) mountLoop(ctx context.Context) {
	ticker := time.NewTicker(m.mountCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			if err := m.syncRoots(ctx, false); err != nil {
				m.log.Error("sync roots", "error", err)
			}
		}
	}
}

func (m *Manager) volumesLoop(ctx context.Context) {
	if _, err := os.Stat("/Volumes"); err != nil {
		return
	}
	eventsCh, err := m.streamFactory(ctx, "/Volumes", m.streamLatency, m.log)
	if err != nil {
		m.log.Warn("watch /Volumes", "error", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-eventsCh:
			if !ok {
				return
			}
			if err := m.syncRoots(ctx, false); err != nil {
				m.log.Error("sync roots after volume event", "error", err)
			}
		}
	}
}

func (m *Manager) syncRoots(ctx context.Context, startup bool) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	roots, err := m.store.ListRoots(ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		online := dirExists(root.Path)
		wasOnline := root.Online
		switch {
		case online:
			if !root.Online {
				if err := m.setRootOnline(ctx, root, true); err != nil {
					return err
				}
				root.Online = true
			}
			m.ensureRootWatcher(ctx, root)
			if startup || !wasOnline {
				m.enqueueReconcile(ctx, root.ID)
			}
		default:
			m.stopRoot(root.ID)
			if root.Online {
				if err := m.setRootOnline(ctx, root, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (m *Manager) ensureRootWatcher(parent context.Context, root store.Root) {
	m.mu.Lock()
	if _, ok := m.runners[root.ID]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.runners[root.ID] = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		// Release the child context even when watchRoot exits on its own
		// (stream failure) — stopRoot never fires for it, and un-cancelled
		// contexts accumulate on the process-lifetime parent.
		defer m.wg.Done()
		defer cancel()
		defer m.removeRootRunner(root.ID)
		m.watchRoot(ctx, root)
	}()
}

func (m *Manager) watchRoot(ctx context.Context, root store.Root) {
	eventsCh, err := m.streamFactory(ctx, root.Path, m.streamLatency, m.log)
	if err != nil {
		m.log.Warn("watch root", "root_id", root.ID, "path", root.Path, "error", err)
		if !dirExists(root.Path) {
			if setErr := m.setRootOnline(context.Background(), root, false); setErr != nil {
				m.log.Error("mark root offline after watch failure", "root_id", root.ID, "error", setErr)
			}
		}
		return
	}

	rw := &rootWatcher{
		manager: m,
		root:    root,
		timers:  make(map[string]*time.Timer),
	}
	defer rw.stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-eventsCh:
			if !ok {
				return
			}
			if ev.Rescan {
				m.enqueueReconcile(ctx, root.ID)
				continue
			}
			rw.schedule(ctx, ev.Path)
		}
	}
}

func (m *Manager) setRootOnline(ctx context.Context, root store.Root, online bool) error {
	if err := m.store.SetRootOnline(ctx, root.ID, online); err != nil {
		return err
	}
	if !online {
		if _, err := m.store.SetRootFilesStatus(ctx, root.ID, "offline"); err != nil {
			return err
		}
	}
	m.publish(events.Event{
		Type:    events.RootStatus,
		Payload: map[string]any{"id": root.ID, "online": online},
	})
	if online {
		m.log.Info("library root online", "root_id", root.ID, "name", root.Name, "path", root.Path)
	} else {
		m.log.Warn("library root offline", "root_id", root.ID, "name", root.Name, "path", root.Path)
	}
	return nil
}

func (m *Manager) stopRoot(rootID int64) {
	m.mu.Lock()
	cancel := m.runners[rootID]
	delete(m.runners, rootID)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.runners))
	for rootID, cancel := range m.runners {
		delete(m.runners, rootID)
		cancels = append(cancels, cancel)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (m *Manager) removeRootRunner(rootID int64) {
	m.mu.Lock()
	delete(m.runners, rootID)
	m.mu.Unlock()
}

func (m *Manager) enqueueReconcile(ctx context.Context, rootID int64) {
	if m.jobs == nil {
		return
	}
	if _, err := m.jobs.EnqueueReconcile(ctx, rootID); err != nil && ctx.Err() == nil {
		m.log.Error("enqueue reconcile", "root_id", rootID, "error", err)
	}
}

func (m *Manager) enqueueProbe(ctx context.Context, rootID int64, relPath string) {
	if m.jobs == nil {
		return
	}
	if _, err := m.jobs.EnqueueProbe(ctx, rootID, relPath); err != nil && ctx.Err() == nil {
		m.log.Error("enqueue probe", "root_id", rootID, "rel_path", relPath, "error", err)
	}
}

func (m *Manager) publish(event events.Event) {
	if m.bus != nil {
		m.bus.Publish(event)
	}
}

func (m *Manager) publishItemUpdated(ctx context.Context, itemID int64) {
	if m.bus == nil {
		return
	}
	summary, err := m.store.GetItemSummary(ctx, itemID)
	if err != nil {
		m.log.Error("hydrate watcher item event", "item_id", itemID, "error", err)
		return
	}
	m.bus.Publish(events.Event{Type: events.ItemUpdated, Payload: summary})
}

type rootWatcher struct {
	manager *Manager
	root    store.Root

	mu       sync.Mutex
	timers   map[string]*time.Timer
	backstop *time.Timer
}

func (rw *rootWatcher) schedule(ctx context.Context, path string) {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return
	}
	rw.mu.Lock()
	if timer := rw.timers[path]; timer != nil {
		timer.Stop()
	}
	rw.timers[path] = time.AfterFunc(rw.manager.debounce, func() {
		rw.mu.Lock()
		delete(rw.timers, path)
		rw.mu.Unlock()
		rw.handlePath(ctx, path)
	})
	// FSEvents can coalesce or silently drop notifications during a bulk copy,
	// leaving the per-file path above short a few files with no rescan flag to
	// recover them. Re-arm a single coalesced reconcile on every event so that
	// once activity settles, one full walk backstops the burst and nothing is
	// lost. It de-dupes in the store, and trickle changes fold into one walk.
	rw.armBackstopLocked(ctx)
	rw.mu.Unlock()
}

func (rw *rootWatcher) armBackstopLocked(ctx context.Context) {
	if rw.backstop != nil {
		rw.backstop.Stop()
	}
	rw.backstop = time.AfterFunc(rw.manager.stormBackstop, func() {
		rw.manager.enqueueReconcile(ctx, rw.root.ID)
	})
}

func (rw *rootWatcher) stop() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.backstop != nil {
		rw.backstop.Stop()
		rw.backstop = nil
	}
	for path, timer := range rw.timers {
		timer.Stop()
		delete(rw.timers, path)
	}
}

func (rw *rootWatcher) handlePath(ctx context.Context, path string) {
	if ctx.Err() != nil {
		return
	}
	if !isUnderRoot(rw.root.Path, path) {
		return
	}
	if rw.manager.ignore.Contains(path) {
		return
	}
	rel, err := relPath(rw.root.Path, path)
	if err != nil {
		rw.manager.log.Warn("watch path outside root", "root_id", rw.root.ID, "path", path, "error", err)
		return
	}
	if ignoredRel(rel) {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			rw.handleRemoved(ctx, rel, path)
			return
		}
		rw.manager.log.Warn("stat watched path", "root_id", rw.root.ID, "path", path, "error", err)
		return
	}
	if info.IsDir() {
		rw.manager.enqueueReconcile(ctx, rw.root.ID)
		return
	}
	if !info.Mode().IsRegular() || !isVideoPath(rel) {
		return
	}
	rw.waitForQuiescence(ctx, rel, path)
}

func (rw *rootWatcher) waitForQuiescence(ctx context.Context, rel, path string) {
	started := time.Now()
	lastSize := int64(-1)
	stableSince := time.Now()
	ticker := time.NewTicker(rw.manager.quiesceInterval)
	defer ticker.Stop()

	for {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				rw.handleRemoved(ctx, rel, path)
				return
			}
			rw.manager.log.Warn("stat quiescing path", "root_id", rw.root.ID, "path", path, "error", err)
			return
		}
		if !info.Mode().IsRegular() {
			return
		}
		now := time.Now()
		if info.Size() != lastSize {
			lastSize = info.Size()
			stableSince = now
		}
		if now.Sub(stableSince) >= rw.manager.quiesceStableFor && canOpen(path) {
			rw.readyFile(ctx, rel, path, info)
			return
		}
		if now.Sub(started) > maxQuiesceAge {
			rw.manager.log.Warn("file never quiesced", "root_id", rw.root.ID, "rel_path", rel)
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (rw *rootWatcher) readyFile(ctx context.Context, rel, path string, info os.FileInfo) {
	fingerprint, err := library.Fingerprint(path)
	if err != nil {
		rw.manager.log.Warn("fingerprint watched file", "root_id", rw.root.ID, "rel_path", rel, "error", err)
		return
	}
	if rw.tryMove(ctx, rel, info, fingerprint) {
		return
	}
	rw.manager.enqueueProbe(ctx, rw.root.ID, rel)
}

func (rw *rootWatcher) tryMove(ctx context.Context, rel string, info os.FileInfo, fingerprint string) bool {
	matches, err := rw.manager.store.GetFilesByFingerprint(ctx, fingerprint)
	if err != nil {
		rw.manager.log.Error("find files by fingerprint", "fingerprint", fingerprint, "error", err)
		return false
	}
	for _, file := range matches {
		if file.Status == "trashed" || (file.RootID == rw.root.ID && file.RelPath == rel) {
			continue
		}
		if file.Status == "online" && rw.filePathStillExists(ctx, file) {
			continue
		}
		if err := rw.manager.store.RelocateFile(ctx, file.ID, rw.root.ID, rel, info.Size(), info.ModTime(), fingerprint); err != nil {
			rw.manager.log.Warn("move file row", "file_id", file.ID, "rel_path", rel, "error", err)
			return false
		}
		rw.manager.publishItemUpdated(ctx, file.ItemID)
		return true
	}
	return false
}

func (rw *rootWatcher) filePathStillExists(ctx context.Context, file store.File) bool {
	root, err := rw.manager.store.GetRoot(ctx, file.RootID)
	if err != nil || !root.Online {
		return false
	}
	path, err := safeJoin(root.Path, file.RelPath)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func (rw *rootWatcher) handleRemoved(ctx context.Context, rel, path string) {
	if !isVideoPath(rel) {
		return
	}
	timer := time.NewTimer(rw.manager.moveWindow)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	file, err := rw.manager.store.GetFileByLocation(ctx, rw.root.ID, rel)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			rw.manager.log.Error("lookup removed file", "root_id", rw.root.ID, "rel_path", rel, "error", err)
		}
		return
	}
	if file.Status == "trashed" {
		return
	}
	if err := rw.manager.store.SetFileStatus(ctx, file.ID, "missing"); err != nil {
		rw.manager.log.Error("mark file missing", "file_id", file.ID, "error", err)
		return
	}
	rw.manager.publishItemUpdated(ctx, file.ItemID)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func relPath(rootPath, path string) (string, error) {
	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		return "", fmt.Errorf("path %q is outside root %q", path, rootPath)
	}
	return rel, nil
}

func isUnderRoot(rootPath, path string) bool {
	rel, err := filepath.Rel(rootPath, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ignoredRel(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func isVideoPath(path string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "mp4", "m4v", "mov", "mkv", "webm", "avi", "ts", "m2ts", "wmv", "flv":
		return true
	default:
		return false
	}
}

func canOpen(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func safeJoin(rootPath, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("invalid relative path %q", rel)
	}
	return filepath.Join(rootPath, clean), nil
}

func withDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
