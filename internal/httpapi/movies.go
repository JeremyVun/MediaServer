package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/classify"
	"github.com/JeremyVun/MediaServer/internal/tmdb"
)

type movieMatchRequest struct {
	TMDBID optional[int64] `json:"tmdb_id"`
}

type movieCandidateResponse struct {
	TMDBID    int64   `json:"tmdb_id"`
	Title     string  `json:"title"`
	Year      *int    `json:"year"`
	PosterURL *string `json:"poster_url"`
}

func (s *Server) handleItemMatchSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetItem(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "q is required")
		return
	}
	year, ok := intQuery(w, r, "year", 0)
	if !ok {
		return
	}
	if !s.tmdb.Configured() {
		writeError(w, http.StatusServiceUnavailable, "tmdb_unavailable", "TMDB is not configured")
		return
	}
	results, err := s.tmdb.SearchMovie(r.Context(), q, year)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tmdb_unavailable", "TMDB could not be reached")
		s.log.Warn("tmdb match search", "q", q, "error", err)
		return
	}
	out := make([]movieCandidateResponse, 0, len(results))
	for _, m := range results {
		c := movieCandidateResponse{
			TMDBID:    int64(m.ID),
			Title:     m.Title,
			PosterURL: nonEmptyString(s.tmdb.ImageURL(tmdb.CandidateSize, m.PosterPath)),
		}
		if m.Year > 0 {
			c.Year = &m.Year
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *Server) handleItemMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req movieMatchRequest
	if !decodeStrict(w, r, &req, "invalid movie match") {
		return
	}
	if !req.TMDBID.Set {
		writeError(w, http.StatusBadRequest, "bad_request", "tmdb_id is required (null for not on TMDB)")
		return
	}
	item, err := s.classifier.ChooseMovieMatch(r.Context(), id, req.TMDBID.Value)
	if errors.Is(err, classify.ErrTMDBNotConfigured) {
		writeError(w, http.StatusServiceUnavailable, "tmdb_unavailable", "TMDB is not configured")
		return
	}
	if err != nil {
		s.writeServiceError(w, "choose movie match", err)
		return
	}
	resp, ok := s.detailResponse(w, r, item)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleItemArt(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	path := s.itemArtPath(id)
	if info, err := os.Stat(path); path == "" || err != nil || !info.Mode().IsRegular() {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusNotFound, "not_found", "no cached art for this item")
		return
	}
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// itemArtURL is the cached backdrop's URL, versioned by its mtime like
// thumbnails, or nil until art_at says it is cached.
func (s *Server) itemArtURL(itemID int64, artAt *string) *string {
	if artAt == nil {
		return nil
	}
	info, err := os.Stat(s.itemArtPath(itemID))
	if err != nil {
		return nil
	}
	u := "/api/items/" + strconv.FormatInt(itemID, 10) + "/art?v=" + strconv.FormatInt(info.ModTime().UnixMilli(), 10)
	return &u
}

func (s *Server) itemArtPath(itemID int64) string {
	if s.classifier == nil {
		return ""
	}
	return s.classifier.ArtPath(itemID)
}
