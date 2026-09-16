package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/JeremyVun/MediaServer/internal/mediapath"
	"net/http"
	"os"

	"github.com/JeremyVun/MediaServer/internal/jobs"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
)

type jobResponse struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	Payload    string  `json:"payload"`
	Status     string  `json:"status"`
	Attempts   int     `json:"attempts"`
	RunAt      string  `json:"run_at"`
	StartedAt  *string `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
	Error      *string `json:"error"`
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit, ok := intQuery(w, r, "limit", 50)
	if !ok {
		return
	}
	jobs, err := s.store.ListJobs(r.Context(), store.ListJobsOpts{
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing jobs failed")
		return
	}
	out := make([]jobResponse, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, jobToResponse(job))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobToResponse(job))
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	job, err := s.store.RetryJob(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if s.jobs != nil {
		s.jobs.Wake()
	}
	writeJSON(w, http.StatusAccepted, jobToResponse(job))
}

// handleTrashJobFile resolves a failed probe/thumbnail job by moving its
// media file into the root's .trash, where the normal retention purge
// (or "Empty trash") deletes it. A file that never probed successfully has
// no catalog row yet; it gets one so the trash section can list, restore
// or purge it like anything else.
func (s *Server) handleTrashJobFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if job.Status != "failed" {
		writeError(w, http.StatusConflict, "job_not_failed", "job is not marked as failed")
		return
	}
	item, ok := s.jobItem(w, r, job)
	if !ok {
		return
	}
	if item.DeletedAt == nil {
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
	}
	if err := s.store.DeleteJob(r.Context(), job.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("delete resolved job", "job_id", job.ID, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"item_id": item.ID})
}

func (s *Server) jobItem(w http.ResponseWriter, r *http.Request, job store.Job) (store.Item, bool) {
	ctx := r.Context()
	switch job.Type {
	case jobs.TypeThumbnail:
		var payload jobs.ThumbnailPayload
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			writeError(w, http.StatusConflict, "job_has_no_file", "this job has no file to remove")
			return store.Item{}, false
		}
		file, err := s.store.GetFile(ctx, payload.FileID)
		if err != nil {
			writeStoreError(w, err)
			return store.Item{}, false
		}
		item, err := s.store.GetItem(ctx, file.ItemID)
		if err != nil {
			writeStoreError(w, err)
			return store.Item{}, false
		}
		return item, true
	case jobs.TypeProbe:
		var payload jobs.ProbePayload
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil || payload.RelPath == "" {
			writeError(w, http.StatusConflict, "job_has_no_file", "this job has no file to remove")
			return store.Item{}, false
		}
		file, err := s.store.GetFileByLocation(ctx, payload.RootID, payload.RelPath)
		if err == nil {
			item, err := s.store.GetItem(ctx, file.ItemID)
			if err != nil {
				writeStoreError(w, err)
				return store.Item{}, false
			}
			return item, true
		}
		if !errors.Is(err, store.ErrNotFound) {
			writeStoreError(w, err)
			return store.Item{}, false
		}
		return s.catalogUnprobedFile(w, r, payload.RootID, payload.RelPath)
	default:
		writeError(w, http.StatusConflict, "job_has_no_file", "this job has no file to remove")
		return store.Item{}, false
	}
}

func (s *Server) catalogUnprobedFile(w http.ResponseWriter, r *http.Request, rootID int64, relPath string) (store.Item, bool) {
	ctx := r.Context()
	root, err := s.store.GetRoot(ctx, rootID)
	if err != nil {
		writeStoreError(w, err)
		return store.Item{}, false
	}
	if !root.Attached || !root.Online || !mediapath.DirExists(root.Path) {
		writeError(w, http.StatusConflict, "root_offline", "root is offline")
		return store.Item{}, false
	}
	path, err := mediapath.SafeJoin(root.Path, relPath)
	if err != nil {
		writeError(w, http.StatusConflict, "invalid_path", "file path is invalid")
		return store.Item{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusConflict, "file_missing", "file is no longer on disk")
		return store.Item{}, false
	}
	fingerprint, err := library.Fingerprint(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "reading file failed")
		s.log.Error("fingerprint unprobed file", "path", path, "error", err)
		return store.Item{}, false
	}
	parsed := library.ParseTitle(relPath)
	item, _, err := s.store.CreateItemWithFile(ctx, store.NewItem{
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
		writeStoreError(w, err)
		return store.Item{}, false
	}
	return item, true
}

func jobToResponse(job store.Job) jobResponse {
	return jobResponse{
		ID:         job.ID,
		Type:       job.Type,
		Payload:    job.Payload,
		Status:     job.Status,
		Attempts:   job.Attempts,
		RunAt:      job.RunAt,
		StartedAt:  job.StartedAt,
		FinishedAt: job.FinishedAt,
		Error:      job.Error,
	}
}
