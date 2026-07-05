package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

type RootLifecycle interface {
	AttachRoot(context.Context, store.Root) error
	DetachRoot(context.Context, store.Root) error
}

type rootResponse struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Online    bool   `json:"online"`
	FreeBytes uint64 `json:"free_bytes"`
	FileCount int64  `json:"file_count"`
}

type addRootRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type rescanResponse struct {
	JobID  int64  `json:"job_id"`
	Status string `json:"status"`
}

type fsDirsResponse struct {
	Path   string       `json:"path"`
	Parent *string      `json:"parent"`
	Dirs   []fsDirEntry `json:"dirs"`
}

type fsDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) handleListRoots(w http.ResponseWriter, r *http.Request) {
	roots, err := s.store.ListRoots(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing roots failed")
		s.log.Error("list roots", "error", err)
		return
	}
	res := make([]rootResponse, 0, len(roots))
	for _, root := range roots {
		rootRes, err := s.rootResponse(r.Context(), root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "loading root stats failed")
			s.log.Error("load root stats", "root_id", root.ID, "error", err)
			return
		}
		res = append(res, rootRes)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAddRoot(w http.ResponseWriter, r *http.Request) {
	var req addRootRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid root")
		return
	}
	path, ok := cleanAbsolutePath(req.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "path_not_absolute", "path must be absolute")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path_not_found", "path must exist and be a directory")
		return
	}
	if duplicate, err := s.hasAttachedRootOverlap(r.Context(), path); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "validating root failed")
		s.log.Error("validate root", "path", path, "error", err)
		return
	} else if duplicate {
		writeError(w, http.StatusConflict, "duplicate_root", "path is already covered by an attached root")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(path)
	}
	root, err := s.store.UpsertRoot(r.Context(), name, path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "saving root failed")
		s.log.Error("save root", "path", path, "error", err)
		return
	}
	if err := s.attachRoot(r.Context(), root); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "starting root failed")
		s.log.Error("attach root", "root_id", root.ID, "path", root.Path, "error", err)
		return
	}
	root, err = s.store.GetRoot(r.Context(), root.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	res, err := s.rootResponse(r.Context(), root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "loading root stats failed")
		s.log.Error("load root stats", "root_id", root.ID, "error", err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleDetachRoot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	root, err := s.store.GetRoot(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !root.Attached {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	activeUploads, err := s.store.CountActiveUploadsForRoot(r.Context(), root.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "checking uploads failed")
		s.log.Error("check active uploads", "root_id", root.ID, "error", err)
		return
	}
	if activeUploads > 0 {
		writeError(w, http.StatusConflict, "active_uploads", "root has active uploads")
		return
	}
	if err := s.detachRoot(r.Context(), root); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "detaching root failed")
		s.log.Error("detach root", "root_id", root.ID, "error", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRootRescan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	root, err := s.store.GetRoot(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !root.Attached {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if !root.Online {
		writeError(w, http.StatusConflict, "root_offline", "root is offline")
		return
	}
	if info, err := os.Stat(root.Path); err != nil || !info.IsDir() {
		// Same transition the watcher performs — including the root.status
		// publish, so connected clients see the flip this rescan discovered.
		_ = s.store.SetRootOnline(r.Context(), root.ID, false)
		_, _ = s.store.SetRootFilesStatus(r.Context(), root.ID, "offline")
		s.publishRootStatus(root.ID, false)
		writeError(w, http.StatusConflict, "root_offline", "root path is not mounted")
		return
	}
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs_unavailable", "job manager is not running")
		return
	}
	job, err := s.jobs.EnqueueReconcile(r.Context(), root.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "enqueue rescan failed")
		s.log.Error("enqueue rescan", "root_id", root.ID, "error", err)
		return
	}
	writeJSON(w, http.StatusAccepted, rescanResponse{JobID: job.ID, Status: job.Status})
}

func (s *Server) handleFSDirs(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		path = "/Volumes"
	}
	cleaned, ok := cleanAbsolutePath(path)
	if !ok {
		writeError(w, http.StatusBadRequest, "path_not_absolute", "path must be absolute")
		return
	}
	entries, err := os.ReadDir(cleaned)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "path not found")
		return
	}
	dirs := make([]fsDirEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		dirs = append(dirs, fsDirEntry{Name: name, Path: filepath.Join(cleaned, name)})
	}
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name < dirs[j].Name
	})
	if len(dirs) > 500 {
		dirs = dirs[:500]
	}
	var parent *string
	if cleaned != string(filepath.Separator) {
		p := filepath.Dir(cleaned)
		parent = &p
	}
	writeJSON(w, http.StatusOK, fsDirsResponse{Path: cleaned, Parent: parent, Dirs: dirs})
}

func (s *Server) rootResponse(ctx context.Context, root store.Root) (rootResponse, error) {
	count, err := s.store.CountFilesForRoot(ctx, root.ID)
	if err != nil {
		return rootResponse{}, err
	}
	var free uint64
	if root.Online {
		free, _ = freeBytes(root.Path)
	}
	return rootResponse{
		ID:        root.ID,
		Name:      root.Name,
		Path:      root.Path,
		Online:    root.Online,
		FreeBytes: free,
		FileCount: count,
	}, nil
}

func (s *Server) attachRoot(ctx context.Context, root store.Root) error {
	if s.rootLifecycle != nil {
		return s.rootLifecycle.AttachRoot(ctx, root)
	}
	if err := s.store.SetRootOnline(ctx, root.ID, true); err != nil {
		return err
	}
	s.publishRootStatus(root.ID, true)
	if s.jobs == nil {
		return nil
	}
	_, err := s.jobs.EnqueueReconcile(ctx, root.ID)
	return err
}

func (s *Server) detachRoot(ctx context.Context, root store.Root) error {
	if s.rootLifecycle != nil {
		return s.rootLifecycle.DetachRoot(ctx, root)
	}
	if err := s.store.DetachRoot(ctx, root.ID); err != nil {
		return err
	}
	s.publishRootStatus(root.ID, false)
	return nil
}

func (s *Server) publishRootStatus(rootID int64, online bool) {
	if s.bus != nil {
		s.bus.Publish(events.Event{
			Type:    events.RootStatus,
			Payload: map[string]any{"id": rootID, "online": online},
		})
	}
}

func (s *Server) hasAttachedRootOverlap(ctx context.Context, path string) (bool, error) {
	roots, err := s.store.ListRoots(ctx)
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		if pathsOverlap(filepath.Clean(root.Path), path) {
			return true, nil
		}
	}
	return false, nil
}

func cleanAbsolutePath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return "", false
	}
	return cleaned, true
}

func pathsOverlap(a, b string) bool {
	return a == b || pathContains(a, b) || pathContains(b, a)
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
