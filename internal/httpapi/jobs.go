package httpapi

import (
	"net/http"

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
