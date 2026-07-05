package httpapi

import (
	"net/http"
	"time"
)

// healthResponse mirrors GET /api/health in SPEC-API.md.
type healthResponse struct {
	Version        string       `json:"version"`
	UptimeS        int64        `json:"uptime_s"`
	DBOK           bool         `json:"db_ok"`
	Roots          []healthRoot `json:"roots"`
	ActiveSessions int          `json:"active_sessions"`
	QueueDepth     int          `json:"queue_depth"`
}

type healthRoot struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	FreeBytes uint64 `json:"free_bytes"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	dbOK := s.store.DB().PingContext(ctx) == nil

	roots, err := s.store.ListRoots(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing roots failed")
		s.log.Error("health: list roots", "error", err)
		return
	}
	hr := make([]healthRoot, 0, len(roots))
	for _, root := range roots {
		var free uint64
		if root.Online {
			free, _ = freeBytes(root.Path) // best effort; 0 when unknown
		}
		hr = append(hr, healthRoot{ID: root.ID, Name: root.Name, Online: root.Online, FreeBytes: free})
	}

	depth, err := s.store.QueueDepth(ctx)
	if err != nil {
		s.log.Error("health: queue depth", "error", err)
		dbOK = false
	}
	activeSessions := 0
	if s.playback != nil {
		activeSessions = s.playback.ActiveSessions()
	}

	writeJSON(w, http.StatusOK, healthResponse{
		Version:        s.version,
		UptimeS:        int64(time.Since(s.startedAt).Seconds()),
		DBOK:           dbOK,
		Roots:          hr,
		ActiveSessions: activeSessions,
		QueueDepth:     depth,
	})
}
