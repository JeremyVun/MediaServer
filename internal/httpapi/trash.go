package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

const trashDirName = ".trash"

type purgeTrashResponse struct {
	Purged  int `json:"purged"`
	Skipped int `json:"skipped"`
}

type trashPlan struct {
	file      store.File
	root      store.Root
	livePath  string
	trashPath string
}

type movedFile struct {
	from string
	to   string
}

func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	item, err := s.store.GetItem(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if item.DeletedAt != nil {
		writeError(w, http.StatusConflict, "already_trashed", "item is already in trash")
		return
	}
	plans, ok := s.trashPlans(w, r, item, false)
	if !ok {
		return
	}
	moved, ok := s.moveToTrash(w, plans)
	if !ok {
		return
	}
	if err := s.store.MarkItemTrashed(r.Context(), item.ID); err != nil {
		rollbackMoves(moved)
		writeStoreError(w, err)
		return
	}
	s.publishRemoved(item.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestoreItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	item, err := s.store.GetItem(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if item.DeletedAt == nil {
		writeError(w, http.StatusConflict, "not_trashed", "item is not in trash")
		return
	}
	plans, ok := s.trashPlans(w, r, item, true)
	if !ok {
		return
	}
	moved, ok := s.restoreFromTrash(w, plans)
	if !ok {
		return
	}
	if err := s.store.MarkItemRestored(r.Context(), item.ID); err != nil {
		rollbackMoves(moved)
		writeStoreError(w, err)
		return
	}
	s.publishItemEvent(r, events.ItemAdded, item.ID)
	writeJSON(w, http.StatusOK, map[string]any{"id": item.ID})
}

func (s *Server) handlePurgeItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	item, err := s.store.GetItem(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if item.DeletedAt == nil {
		writeError(w, http.StatusConflict, "not_trashed", "item is not in trash")
		return
	}
	if ok := s.purgeItem(w, r.Context(), item, false); !ok {
		return
	}
	s.publishRemoved(item.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePurgeTrash(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListTrashedItemsBefore(r.Context(), timeNow(), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing trash failed")
		s.log.Error("list trash for purge", "error", err)
		return
	}
	var res purgeTrashResponse
	for _, item := range items {
		if ok := s.purgeItem(w, r.Context(), item, true); ok {
			res.Purged++
			s.publishRemoved(item.ID)
		} else {
			res.Skipped++
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) trashPlans(w http.ResponseWriter, r *http.Request, item store.Item, restoring bool) ([]trashPlan, bool) {
	files, err := s.store.ListFilesForItem(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing files failed")
		s.log.Error("list item files for trash", "item_id", item.ID, "error", err)
		return nil, false
	}
	plans := make([]trashPlan, 0, len(files))
	for _, file := range files {
		root, err := s.store.GetRoot(r.Context(), file.RootID)
		if err != nil {
			writeStoreError(w, err)
			return nil, false
		}
		if !root.Attached || !root.Online || !dirExistsForHTTP(root.Path) {
			writeError(w, http.StatusConflict, "root_offline", "root is offline")
			return nil, false
		}
		if restoring {
			if file.Status != "trashed" {
				writeError(w, http.StatusConflict, "not_trashed", "file is not in trash")
				return nil, false
			}
		} else if file.Status != "online" {
			writeError(w, http.StatusConflict, "file_unavailable", "file is not online")
			return nil, false
		}
		livePath, err := mediaFilePath(root.Path, file.RelPath)
		if err != nil {
			writeError(w, http.StatusConflict, "invalid_path", "file path is invalid")
			return nil, false
		}
		plans = append(plans, trashPlan{
			file:      file,
			root:      root,
			livePath:  livePath,
			trashPath: trashedFilePath(root.Path, file),
		})
	}
	return plans, true
}

func (s *Server) moveToTrash(w http.ResponseWriter, plans []trashPlan) ([]movedFile, bool) {
	moved := []movedFile{}
	for _, plan := range plans {
		if info, err := os.Stat(plan.livePath); err != nil || !info.Mode().IsRegular() {
			rollbackMoves(moved)
			writeError(w, http.StatusConflict, "file_missing", "file is missing from disk")
			return nil, false
		}
		if err := os.MkdirAll(filepath.Dir(plan.trashPath), 0o755); err != nil {
			rollbackMoves(moved)
			writeError(w, http.StatusInternalServerError, "internal", "creating trash failed")
			return nil, false
		}
		if _, err := os.Stat(plan.trashPath); err == nil {
			rollbackMoves(moved)
			writeError(w, http.StatusConflict, "trash_conflict", "trash destination already exists")
			return nil, false
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackMoves(moved)
			writeError(w, http.StatusInternalServerError, "internal", "checking trash failed")
			return nil, false
		}
		srcRelease := s.ignorePath(plan.livePath)
		dstRelease := s.ignorePath(plan.trashPath)
		if err := os.Rename(plan.livePath, plan.trashPath); err != nil {
			srcRelease()
			dstRelease()
			rollbackMoves(moved)
			writeError(w, http.StatusInternalServerError, "internal", "moving file to trash failed")
			return nil, false
		}
		holdIgnore(srcRelease)
		holdIgnore(dstRelease)
		moved = append(moved, movedFile{from: plan.livePath, to: plan.trashPath})
	}
	return moved, true
}

func (s *Server) restoreFromTrash(w http.ResponseWriter, plans []trashPlan) ([]movedFile, bool) {
	moved := []movedFile{}
	for _, plan := range plans {
		if _, err := os.Stat(plan.trashPath); err != nil {
			rollbackMoves(moved)
			writeError(w, http.StatusConflict, "trash_missing", "trashed file is missing")
			return nil, false
		}
		if _, err := os.Stat(plan.livePath); err == nil {
			rollbackMoves(moved)
			writeError(w, http.StatusConflict, "restore_conflict", "original path is occupied")
			return nil, false
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackMoves(moved)
			writeError(w, http.StatusInternalServerError, "internal", "checking restore path failed")
			return nil, false
		}
		if err := os.MkdirAll(filepath.Dir(plan.livePath), 0o755); err != nil {
			rollbackMoves(moved)
			writeError(w, http.StatusInternalServerError, "internal", "creating restore directory failed")
			return nil, false
		}
		trashRelease := s.ignorePath(plan.trashPath)
		liveRelease := s.ignorePath(plan.livePath)
		if err := os.Rename(plan.trashPath, plan.livePath); err != nil {
			trashRelease()
			liveRelease()
			rollbackMoves(moved)
			writeError(w, http.StatusInternalServerError, "internal", "restoring file failed")
			return nil, false
		}
		holdIgnore(trashRelease)
		holdIgnore(liveRelease)
		moved = append(moved, movedFile{from: plan.trashPath, to: plan.livePath})
	}
	return moved, true
}

func (s *Server) purgeItem(w http.ResponseWriter, ctx context.Context, item store.Item, skipUnavailable bool) bool {
	files, err := s.store.ListFilesForItem(ctx, item.ID)
	if err != nil {
		if !skipUnavailable {
			writeError(w, http.StatusInternalServerError, "internal", "listing files failed")
		}
		return false
	}
	for _, file := range files {
		root, err := s.store.GetRoot(ctx, file.RootID)
		if err != nil {
			if !skipUnavailable {
				writeStoreError(w, err)
			}
			return false
		}
		if !root.Online || !dirExistsForHTTP(root.Path) {
			if !skipUnavailable {
				writeError(w, http.StatusConflict, "root_offline", "root is offline")
			}
			return false
		}
		path := trashedFilePath(root.Path, file)
		release := s.ignorePath(path)
		err = os.Remove(path)
		release()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			if !skipUnavailable {
				writeError(w, http.StatusInternalServerError, "internal", "purging trash failed")
			}
			return false
		}
	}
	if err := s.store.HardDeleteItem(ctx, item.ID); err != nil {
		if !skipUnavailable {
			writeStoreError(w, err)
		}
		return false
	}
	return true
}

func (s *Server) publishRemoved(itemID int64) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: events.ItemRemoved, Payload: map[string]any{"id": itemID}})
	}
}

func mediaFilePath(rootPath, relPath string) (string, error) {
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative media path %q", relPath)
	}
	return filepath.Join(rootPath, rel), nil
}

func trashedFilePath(rootPath string, file store.File) string {
	name := filepath.Base(filepath.FromSlash(file.RelPath))
	return filepath.Join(rootPath, trashDirName, fmt.Sprintf("%d_%s", file.ID, name))
}

func dirExistsForHTTP(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func rollbackMoves(moved []movedFile) {
	for i := len(moved) - 1; i >= 0; i-- {
		_ = os.MkdirAll(filepath.Dir(moved[i].from), 0o755)
		_ = os.Rename(moved[i].to, moved[i].from)
	}
}

func timeNow() time.Time {
	return time.Now().UTC()
}
