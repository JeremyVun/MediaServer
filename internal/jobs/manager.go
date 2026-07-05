package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/probe"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/thumbs"
)

const (
	TypeProbe     = "probe"
	TypeThumbnail = "thumbnail"
	TypeReconcile = "reconcile"
	TypeCleanup   = "cleanup"

	maxAttempts = 3
)

type CachePruner interface {
	PruneCache(context.Context) error
}

type Manager struct {
	store *store.Store
	bus   *events.Bus
	log   *slog.Logger

	probe  probe.Runner
	thumbs thumbs.Generator
	hls    CachePruner

	trashRetentionDays int
	uploadMaxAge       time.Duration
	cleanupInterval    time.Duration

	workers int
	nudge   chan struct{}
	wg      sync.WaitGroup

	// reconcileMu serializes reconcile execution per root: the mount monitor
	// and /Volumes watcher can both enqueue for the same root, and with two
	// workers those jobs could otherwise walk the same tree concurrently.
	reconcileMu    sync.Mutex
	reconcileLocks map[int64]*sync.Mutex
}

type Options struct {
	Store     *store.Store
	Bus       *events.Bus
	Log       *slog.Logger
	FFprobe   string
	FFmpeg    string
	ThumbsDir string
	Workers   int

	TrashRetentionDays int
	UploadMaxAge       time.Duration
	CleanupInterval    time.Duration
	HLSPruner          CachePruner
}

type ReconcilePayload struct {
	RootID int64 `json:"root_id"`
}

type ProbePayload struct {
	RootID  int64  `json:"root_id"`
	RelPath string `json:"rel_path"`
}

type ThumbnailPayload struct {
	FileID int64 `json:"file_id"`
}

type CleanupPayload struct{}

func NewManager(opts Options) *Manager {
	workers := opts.Workers
	if workers <= 0 {
		workers = 2
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.UploadMaxAge <= 0 {
		opts.UploadMaxAge = 7 * 24 * time.Hour
	}
	return &Manager{
		store:              opts.Store,
		bus:                opts.Bus,
		log:                opts.Log,
		probe:              probe.Runner{Binary: opts.FFprobe, Log: opts.Log},
		thumbs:             thumbs.Generator{Binary: opts.FFmpeg, Dir: opts.ThumbsDir, Log: opts.Log},
		hls:                opts.HLSPruner,
		trashRetentionDays: opts.TrashRetentionDays,
		uploadMaxAge:       opts.UploadMaxAge,
		cleanupInterval:    opts.CleanupInterval,
		workers:            workers,
		nudge:              make(chan struct{}, 1),
		reconcileLocks:     make(map[int64]*sync.Mutex),
	}
}

func (m *Manager) Start(ctx context.Context) {
	if n, err := m.store.ResetRunningJobs(ctx); err != nil {
		m.log.Error("reset running jobs", "error", err)
	} else if n > 0 {
		m.log.Warn("reset abandoned running jobs", "count", n)
	}
	if m.cleanupInterval > 0 {
		if _, err := m.EnqueueCleanup(ctx, time.Now()); err != nil {
			m.log.Error("enqueue cleanup", "error", err)
		}
	}
	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go m.worker(ctx, i)
	}
}

// Wait blocks until every worker goroutine has exited. Workers stop when the
// context passed to Start is cancelled, finishing or abandoning their current
// job first (an abandoned job stays 'running' and is requeued on next boot).
// Call after cancelling that context and before closing the store's DB.
func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) EnqueueReconcile(ctx context.Context, rootID int64) (store.Job, error) {
	return m.enqueue(ctx, TypeReconcile, ReconcilePayload{RootID: rootID})
}

func (m *Manager) EnqueueProbe(ctx context.Context, rootID int64, relPath string) (store.Job, error) {
	return m.enqueue(ctx, TypeProbe, ProbePayload{RootID: rootID, RelPath: relPath})
}

func (m *Manager) EnqueueCleanup(ctx context.Context, runAt time.Time) (store.Job, error) {
	raw, err := json.Marshal(CleanupPayload{})
	if err != nil {
		return store.Job{}, err
	}
	job, err := m.store.EnqueueJobAt(ctx, TypeCleanup, string(raw), runAt)
	if err != nil {
		return store.Job{}, err
	}
	m.wake()
	return job, nil
}

func (m *Manager) enqueueProbe(ctx context.Context, rootID int64, relPath string) (store.Job, error) {
	return m.EnqueueProbe(ctx, rootID, relPath)
}

func (m *Manager) enqueueThumbnail(ctx context.Context, fileID int64) (store.Job, error) {
	return m.enqueue(ctx, TypeThumbnail, ThumbnailPayload{FileID: fileID})
}

func (m *Manager) enqueue(ctx context.Context, typ string, payload any) (store.Job, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return store.Job{}, err
	}
	job, err := m.store.EnqueueJob(ctx, typ, string(raw))
	if err != nil {
		return store.Job{}, err
	}
	m.wake()
	return job, nil
}

func (m *Manager) wake() {
	select {
	case m.nudge <- struct{}{}:
	default:
	}
}

func (m *Manager) Wake() {
	m.wake()
}

func (m *Manager) worker(ctx context.Context, index int) {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		job, err := m.store.ClaimNextJob(ctx)
		if err == nil {
			m.runJob(ctx, job)
			continue
		}
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			m.log.Error("claim job", "worker", index, "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-m.nudge:
		case <-ticker.C:
		}
	}
}

func (m *Manager) runJob(ctx context.Context, job store.Job) {
	err := m.handleJob(ctx, job)
	if err != nil && ctx.Err() != nil {
		// Shutting down: leave the job 'running'; startup recovery
		// (ResetRunningJobs) requeues it on next boot.
		return
	}
	if err == nil {
		if markErr := m.store.MarkJobDone(ctx, job.ID); markErr != nil {
			m.log.Error("mark job done", "job_id", job.ID, "error", markErr)
		}
		m.publish(events.Event{Type: events.JobProgress, Payload: map[string]any{
			"job_id": job.ID,
			"type":   job.Type,
			"pct":    100,
		}})
		if job.Type == TypeCleanup && m.cleanupInterval > 0 && ctx.Err() == nil {
			if _, enqueueErr := m.EnqueueCleanup(ctx, time.Now().Add(m.cleanupInterval)); enqueueErr != nil {
				m.log.Error("schedule cleanup", "error", enqueueErr)
			}
		}
		return
	}

	attempts := job.Attempts + 1
	message := truncateError(err.Error())
	var permanent permanentError
	if errors.As(err, &permanent) || attempts >= maxAttempts {
		if markErr := m.store.MarkJobFailed(ctx, job.ID, attempts, message); markErr != nil {
			m.log.Error("mark job failed", "job_id", job.ID, "error", markErr)
		}
		m.log.Warn("job failed", "job_id", job.ID, "type", job.Type, "error", message)
		return
	}

	runAt := time.Now().Add(backoff(attempts))
	if rescheduleErr := m.store.RescheduleJob(ctx, job.ID, attempts, runAt, message); rescheduleErr != nil {
		m.log.Error("reschedule job", "job_id", job.ID, "error", rescheduleErr)
	}
}

func (m *Manager) handleJob(ctx context.Context, job store.Job) error {
	switch job.Type {
	case TypeReconcile:
		var payload ReconcilePayload
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return Permanent(fmt.Errorf("decode reconcile payload: %w", err))
		}
		return m.handleReconcile(ctx, payload.RootID)
	case TypeProbe:
		var payload ProbePayload
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return Permanent(fmt.Errorf("decode probe payload: %w", err))
		}
		return m.handleProbe(ctx, payload.RootID, payload.RelPath)
	case TypeThumbnail:
		var payload ThumbnailPayload
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return Permanent(fmt.Errorf("decode thumbnail payload: %w", err))
		}
		return m.handleThumbnail(ctx, payload.FileID)
	case TypeCleanup:
		var payload CleanupPayload
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return Permanent(fmt.Errorf("decode cleanup payload: %w", err))
		}
		return m.handleCleanup(ctx)
	default:
		return Permanent(fmt.Errorf("unknown job type %q", job.Type))
	}
}

func (m *Manager) handleReconcile(ctx context.Context, rootID int64) error {
	lock := m.rootReconcileLock(rootID)
	lock.Lock()
	defer lock.Unlock()

	root, err := m.store.GetRoot(ctx, rootID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Permanent(err)
		}
		return err
	}
	if !root.Online {
		return Permanent(fmt.Errorf("root %d is offline", root.ID))
	}
	if info, err := os.Stat(root.Path); err != nil || !info.IsDir() {
		_ = m.store.SetRootOnline(ctx, root.ID, false)
		_, _ = m.store.SetRootFilesStatus(ctx, root.ID, "offline")
		return Permanent(fmt.Errorf("root %d path is offline: %s", root.ID, root.Path))
	}

	seen := map[string]bool{}
	err = filepath.WalkDir(root.Path, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root.Path && d.IsDir() && isIgnoredDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") || !isVideoPath(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root.Path, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true

		existing, err := m.store.GetFileByLocation(ctx, root.ID, rel)
		if err == nil {
			if existing.Status != "online" {
				if err := m.store.SetFileStatus(ctx, existing.ID, "online"); err != nil {
					return err
				}
				m.publish(events.Event{Type: events.FileStatus, Payload: map[string]any{
					"file_id": existing.ID,
					"status":  "online",
				}})
				m.publishItemSummary(ctx, existing.ItemID)
			}
			// Unchanged and already probed: a remount only needs the status
			// flip above, not a re-probe of the whole library.
			if existing.Size == info.Size() && existing.Mtime == store.FormatTime(info.ModTime()) &&
				existing.ProbedAt != nil {
				return nil
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		} else {
			fingerprint, err := library.Fingerprint(path)
			if err != nil {
				return err
			}
			moved, err := m.moveByFingerprint(ctx, root, rel, info, fingerprint)
			if err != nil {
				return err
			}
			if moved {
				return nil
			}
		}

		_, err = m.enqueueProbe(ctx, root.ID, rel)
		return err
	})
	if err != nil {
		return err
	}

	files, err := m.store.ListFilesForRoot(ctx, root.ID)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Status == "trashed" || seen[file.RelPath] {
			continue
		}
		if err := m.store.SetFileStatus(ctx, file.ID, "missing"); err != nil {
			return err
		}
		m.publish(events.Event{Type: events.FileStatus, Payload: map[string]any{
			"file_id": file.ID,
			"status":  "missing",
		}})
		m.publishItemSummary(ctx, file.ItemID)
	}
	return nil
}

func (m *Manager) handleProbe(ctx context.Context, rootID int64, relPath string) error {
	root, err := m.store.GetRoot(ctx, rootID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Permanent(err)
		}
		return err
	}
	if !root.Online {
		return Permanent(fmt.Errorf("root %d is offline", root.ID))
	}
	mediaPath, err := safeJoin(root.Path, relPath)
	if err != nil {
		return Permanent(err)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		return Permanent(fmt.Errorf("stat media file: %w", err))
	}
	if !info.Mode().IsRegular() {
		return Permanent(fmt.Errorf("%s is not a regular file", relPath))
	}

	probed, err := m.probe.Probe(ctx, mediaPath)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return err // transient (busy disk): retry with backoff
		}
		return Permanent(err) // corrupt/unreadable media: retrying won't help
	}
	fingerprint, err := library.Fingerprint(mediaPath)
	if err != nil {
		return err
	}

	existing, err := m.store.GetFileByLocation(ctx, root.ID, relPath)
	newFile := errors.Is(err, store.ErrNotFound)
	if err != nil && !newFile {
		return err
	}

	var item store.Item
	var file store.File
	if newFile {
		parsed := library.ParseTitle(relPath)
		item, file, err = m.store.CreateItemWithFile(ctx, store.NewItem{
			Type:  parsed.Type,
			Title: parsed.Title,
			Year:  parsed.Year,
		}, store.NewFile{
			RootID:      root.ID,
			RelPath:     relPath,
			Size:        info.Size(),
			Mtime:       info.ModTime(),
			Fingerprint: fingerprint,
		})
		if err != nil {
			return err
		}
	} else {
		file = existing
		item, err = m.store.GetItem(ctx, file.ItemID)
		if err != nil {
			return err
		}
		if err := m.store.UpdateFileStat(ctx, file.ID, info.Size(), info.ModTime(), fingerprint); err != nil {
			return err
		}
		if file.Status != "online" {
			if err := m.store.SetFileStatus(ctx, file.ID, "online"); err != nil {
				return err
			}
		}
	}

	if err := m.store.UpdateFileProbe(ctx, file.ID, store.ProbeResult{
		Container: probed.Container,
		DurationS: probed.DurationS,
		Bitrate:   probed.Bitrate,
		Width:     probed.Width,
		Height:    probed.Height,
	}); err != nil {
		return err
	}
	streams := make([]store.Stream, 0, len(probed.Streams))
	for _, st := range probed.Streams {
		streams = append(streams, store.Stream{
			FileID:      file.ID,
			StreamIndex: st.Index,
			Kind:        st.Kind,
			Codec:       st.Codec,
			Lang:        st.Lang,
			Title:       st.Title,
			Channels:    st.Channels,
			IsDefault:   st.IsDefault,
		})
	}
	if err := m.store.ReplaceFileStreams(ctx, file.ID, streams); err != nil {
		return err
	}
	if _, err := m.enqueueThumbnail(ctx, file.ID); err != nil {
		return err
	}

	eventType := events.ItemUpdated
	if newFile {
		eventType = events.ItemAdded
	}
	// Publish the full summary (SPEC-API sends whole item objects over SSE);
	// the job itself succeeded, so a hydration failure only costs the event.
	summary, err := m.store.GetItemSummary(ctx, item.ID)
	if err != nil {
		m.log.Error("hydrate item event", "item_id", item.ID, "error", err)
		return nil
	}
	m.publish(events.Event{Type: eventType, Payload: summary})
	m.publishUploadHandoff(ctx, root.ID, relPath, item.ID)
	return nil
}

func (m *Manager) handleThumbnail(ctx context.Context, fileID int64) error {
	file, err := m.store.GetFile(ctx, fileID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Permanent(err)
		}
		return err
	}
	root, err := m.store.GetRoot(ctx, file.RootID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Permanent(err)
		}
		return err
	}
	if !root.Online || file.Status != "online" {
		return Permanent(fmt.Errorf("file %d is not online", file.ID))
	}
	mediaPath, err := safeJoin(root.Path, file.RelPath)
	if err != nil {
		return Permanent(err)
	}
	var duration float64
	if file.DurationS != nil {
		duration = *file.DurationS
	}
	if err := m.thumbs.Generate(ctx, file.ID, mediaPath, duration); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return err // transient (busy disk): retry with backoff
		}
		return Permanent(err)
	}
	// The summary's thumb_url gains its ?v= version now that the file exists;
	// clients swap their placeholder for the real image on this event.
	m.publishItemSummary(ctx, file.ItemID)
	return nil
}

func (m *Manager) handleCleanup(ctx context.Context) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -m.trashRetentionDays)
	trashed, err := m.store.ListTrashedItemsBefore(ctx, cutoff, 200)
	if err != nil {
		return err
	}
	purgedTrash := 0
	skippedTrash := 0
	for _, item := range trashed {
		ok, err := m.purgeTrashedItem(ctx, item.ID)
		if err != nil {
			return err
		}
		if ok {
			purgedTrash++
			m.publish(events.Event{Type: events.ItemRemoved, Payload: map[string]any{"id": item.ID}})
		} else {
			skippedTrash++
		}
	}

	if m.hls != nil {
		if err := m.hls.PruneCache(ctx); err != nil {
			return err
		}
	}

	uploads, err := m.store.ListUploadsBefore(ctx, time.Now().UTC().Add(-m.uploadMaxAge), 200)
	if err != nil {
		return err
	}
	purgedUploads := 0
	for _, upload := range uploads {
		ok, err := m.purgeStaleUpload(ctx, upload)
		if err != nil {
			return err
		}
		if ok {
			purgedUploads++
		}
	}
	m.log.Debug("cleanup complete", "trash_purged", purgedTrash, "trash_skipped", skippedTrash, "uploads_purged", purgedUploads)
	return nil
}

func (m *Manager) purgeTrashedItem(ctx context.Context, itemID int64) (bool, error) {
	files, err := m.store.ListFilesForItem(ctx, itemID)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		root, err := m.store.GetRoot(ctx, file.RootID)
		if err != nil {
			return false, err
		}
		if !root.Online || !dirExists(root.Path) {
			return false, nil
		}
		if err := os.Remove(trashedFilePath(root.Path, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if err := m.store.HardDeleteItem(ctx, itemID); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) purgeStaleUpload(ctx context.Context, upload store.Upload) (bool, error) {
	if upload.Status == "complete" {
		if err := m.store.DeleteUpload(ctx, upload.ID); err != nil {
			return false, err
		}
		return true, nil
	}

	root, err := m.store.GetRoot(ctx, upload.RootID)
	if err != nil {
		return false, err
	}
	if !root.Online || !dirExists(root.Path) {
		return false, nil
	}
	partPath := filepath.Join(root.Path, "incoming", ".uploads", upload.ID+".part")
	if err := os.Remove(partPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := m.store.DeleteUpload(ctx, upload.ID); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) rootReconcileLock(rootID int64) *sync.Mutex {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	lock, ok := m.reconcileLocks[rootID]
	if !ok {
		lock = &sync.Mutex{}
		m.reconcileLocks[rootID] = lock
	}
	return lock
}

func trashedFilePath(rootPath string, file store.File) string {
	name := filepath.Base(filepath.FromSlash(file.RelPath))
	return filepath.Join(rootPath, ".trash", fmt.Sprintf("%d_%s", file.ID, name))
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (m *Manager) moveByFingerprint(ctx context.Context, targetRoot store.Root, relPath string, info fs.FileInfo, fingerprint string) (bool, error) {
	matches, err := m.store.GetFilesByFingerprint(ctx, fingerprint)
	if err != nil {
		return false, err
	}
	for _, file := range matches {
		if file.Status == "trashed" || (file.RootID == targetRoot.ID && file.RelPath == relPath) {
			continue
		}
		if m.filePathExists(ctx, file) {
			continue
		}
		if err := m.store.RelocateFile(ctx, file.ID, targetRoot.ID, relPath, info.Size(), info.ModTime(), fingerprint); err != nil {
			return false, err
		}
		m.publish(events.Event{Type: events.FileStatus, Payload: map[string]any{
			"file_id": file.ID,
			"status":  "online",
		}})
		m.publishItemSummary(ctx, file.ItemID)
		return true, nil
	}
	return false, nil
}

func (m *Manager) filePathExists(ctx context.Context, file store.File) bool {
	root, err := m.store.GetRoot(ctx, file.RootID)
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

func (m *Manager) publish(event events.Event) {
	if m.bus != nil {
		m.bus.Publish(event)
	}
}

func (m *Manager) publishItemSummary(ctx context.Context, itemID int64) {
	if m.bus == nil {
		return
	}
	summary, err := m.store.GetItemSummary(ctx, itemID)
	if err != nil {
		m.log.Error("hydrate item event", "item_id", itemID, "error", err)
		return
	}
	m.publish(events.Event{Type: events.ItemUpdated, Payload: summary})
}

func (m *Manager) publishUploadHandoff(ctx context.Context, rootID int64, relPath string, itemID int64) {
	if m.bus == nil {
		return
	}
	uploads, err := m.store.AttachUploadItem(ctx, rootID, relPath, itemID)
	if err != nil {
		m.log.Error("attach upload item", "root_id", rootID, "rel_path", relPath, "item_id", itemID, "error", err)
		return
	}
	for _, upload := range uploads {
		m.publish(events.Event{
			Type: events.UploadComplete,
			Payload: map[string]any{
				"id":      upload.ID,
				"item_id": itemID,
			},
		})
	}
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func Permanent(err error) error {
	return permanentError{err: err}
}

func backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	return time.Duration(math.Pow(4, float64(attempts-1))) * time.Minute
}

func truncateError(message string) string {
	const max = 2000
	if len(message) <= max {
		return message
	}
	return message[:max]
}

func isIgnoredDir(name string) bool {
	return strings.HasPrefix(name, ".")
}

func isVideoPath(name string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "mp4", "m4v", "mov", "mkv", "webm", "avi", "ts", "m2ts", "wmv", "flv":
		return true
	default:
		return false
	}
}

func safeJoin(rootPath, relPath string) (string, error) {
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("invalid relative media path %q", relPath)
	}
	return filepath.Join(rootPath, rel), nil
}
