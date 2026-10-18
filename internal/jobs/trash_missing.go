package jobs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/mediapath"
	"github.com/JeremyVun/MediaServer/internal/store"
)

const (
	// missingGrace is how long a file stays missing before its item moves to
	// Trash, so a rename or move can re-pair it by fingerprint first.
	missingGrace = 10 * time.Minute
	// massVanishPercent: past this share of a root's files missing at once
	// the volume is suspect (TCC revocation, an exFAT hiccup), not the owner.
	massVanishPercent = 20
)

// handleTrashMissing is ruling B16: an item whose files are all missing from
// online, readable roots for longer than missingGrace moves to Trash. Only
// catalog rows change; no file on disk is touched.
func (m *Manager) handleTrashMissing(ctx context.Context) error {
	roots, err := m.store.ListRoots(ctx)
	if err != nil {
		return err
	}
	// Holding every root's reconcile lock keeps the sweep out of the middle
	// of a walk that may be about to re-pair a moved file.
	for _, root := range roots {
		lock := m.rootReconcileLock(root.ID)
		lock.Lock()
		defer lock.Unlock()
	}

	now := m.now()
	cutoff := now.Add(-missingGrace)
	counts, err := m.store.CountLiveFilesByRoot(ctx)
	if err != nil {
		return err
	}
	safe := m.rootsSafeToTrashFrom(roots, counts)
	items, err := m.store.ListItemsMissingSince(ctx, cutoff)
	if err != nil {
		return err
	}
	shows := map[int64]bool{}
	for _, item := range items {
		files, err := m.store.ListFilesForItem(ctx, item.ID)
		if err != nil {
			return err
		}
		if !m.filesGone(files, safe) {
			continue
		}
		trashed, err := m.store.TrashMissingItem(ctx, item.ID, cutoff)
		if err != nil {
			return err
		}
		if !trashed {
			continue
		}
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.RelPath
		}
		m.log.Info("moved to trash: files deleted from disk", "item_id", item.ID, "title", item.Title, "files", paths)
		m.publish(events.Event{Type: events.ItemRemoved, Payload: map[string]any{"id": item.ID}})
		if item.ShowID != nil {
			shows[*item.ShowID] = true
		}
	}
	for showID := range shows {
		m.publish(events.Event{Type: events.ShowUpdated, Payload: map[string]any{"id": showID}})
	}

	next, ok, err := m.store.NextMissingSince(ctx, cutoff)
	if err != nil || !ok {
		return err
	}
	// missing_since has whole-second precision, so wake a second late.
	_, err = m.enqueueAt(ctx, TypeTrashMissing, TrashMissingPayload{}, next.Add(missingGrace+time.Second))
	return err
}

// rootsSafeToTrashFrom returns the roots whose missing files can be believed:
// online, listable right now, and not missing so many files that the volume
// itself is the likelier cause.
func (m *Manager) rootsSafeToTrashFrom(roots []store.Root, counts map[int64]store.RootFileCounts) map[int64]store.Root {
	safe := map[int64]store.Root{}
	for _, root := range roots {
		c := counts[root.ID]
		if c.Missing == 0 || !root.Online || !mediapath.DirExists(root.Path) {
			continue
		}
		if err := listable(root.Path); err != nil {
			m.log.Warn("not trashing missing files: library root unreadable",
				"root_id", root.ID, "name", root.Name, "path", root.Path, "error", err)
			continue
		}
		if c.Missing*100 > c.Live*massVanishPercent {
			m.log.Warn("not trashing missing files: too many missing on library root",
				"root_id", root.ID, "name", root.Name, "path", root.Path,
				"missing", c.Missing, "files", c.Live, "limit_percent", massVanishPercent)
			continue
		}
		safe[root.ID] = root
	}
	return safe
}

// filesGone re-checks each file on disk at decision time: only a definite
// "no such file" counts, never a permission or I/O error.
func (m *Manager) filesGone(files []store.File, roots map[int64]store.Root) bool {
	for _, f := range files {
		root, ok := roots[f.RootID]
		if !ok {
			return false
		}
		path, err := mediapath.SafeJoin(root.Path, f.RelPath)
		if err != nil {
			return false
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				m.log.Info("not trashing: file marked missing is on disk", "file_id", f.ID, "rel_path", f.RelPath)
			} else {
				m.log.Warn("not trashing: cannot check missing file", "file_id", f.ID, "rel_path", f.RelPath, "error", err)
			}
			return false
		}
	}
	return true
}

func listable(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
