package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/thumbs"
)

type itemListResponse struct {
	Total int                   `json:"total"`
	Items []itemSummaryResponse `json:"items"`
}

type itemSummaryResponse struct {
	ID            int64             `json:"id"`
	Type          string            `json:"type"`
	Title         string            `json:"title"`
	Year          *int              `json:"year"`
	DurationS     *float64          `json:"duration_s"`
	CreatedAt     string            `json:"created_at"`
	ThumbURL      string            `json:"thumb_url"`
	Available     bool              `json:"available"`
	Progress      *progressResponse `json:"progress,omitempty"`
	CollectionIDs []int64           `json:"collection_ids"`
}

type progressResponse struct {
	PositionS float64 `json:"position_s"`
	Completed bool    `json:"completed"`
}

type itemDetailResponse struct {
	ID            int64             `json:"id"`
	Type          string            `json:"type"`
	Title         string            `json:"title"`
	Year          *int              `json:"year"`
	Summary       *string           `json:"summary"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	DeletedAt     *string           `json:"deleted_at"`
	CollectionIDs []int64           `json:"collection_ids"`
	Progress      *progressResponse `json:"progress,omitempty"`
	Files         []fileResponse    `json:"files"`
}

type fileResponse struct {
	ID        int64            `json:"id"`
	RootID    int64            `json:"root_id"`
	RelPath   string           `json:"rel_path"`
	Size      int64            `json:"size"`
	Container *string          `json:"container"`
	DurationS *float64         `json:"duration_s"`
	Width     *int             `json:"width"`
	Height    *int             `json:"height"`
	Bitrate   *int64           `json:"bitrate"`
	Status    string           `json:"status"`
	Streams   []streamResponse `json:"streams"`
}

type streamResponse struct {
	StreamIndex int     `json:"stream_index"`
	Kind        string  `json:"kind"`
	Codec       string  `json:"codec"`
	Lang        *string `json:"lang,omitempty"`
	Title       *string `json:"title,omitempty"`
	Channels    *int    `json:"channels,omitempty"`
	IsDefault   bool    `json:"is_default"`
}

type patchItemRequest struct {
	Type    *string `json:"type"`
	Title   *string `json:"title"`
	Year    *int    `json:"year"`
	Summary *string `json:"summary"`
}

type progressRequest struct {
	PositionS float64 `json:"position_s"`
	DurationS float64 `json:"duration_s"`
}

func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	opts, ok := listItemsOptsFromRequest(w, r)
	if !ok {
		return
	}
	items, total, err := s.store.ListItemSummaries(r.Context(), opts)
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "listing items failed")
		s.log.Error("list items", "error", err)
		return
	}
	writeJSON(w, http.StatusOK, itemListResponse{Total: total, Items: s.itemSummaries(items)})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	limit, ok := intQuery(w, r, "limit", 20)
	if !ok {
		return
	}
	items, total, err := s.store.SearchItemSummaries(r.Context(), store.SearchItemsOpts{
		Query:        r.URL.Query().Get("q"),
		Type:         r.URL.Query().Get("type"),
		CollectionID: int64(collectionIDQuery(r)),
		Uncollected:  boolQuery(r, "uncollected"),
		Limit:        limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		s.log.Error("search items", "error", err)
		return
	}
	writeJSON(w, http.StatusOK, itemListResponse{Total: total, Items: s.itemSummaries(items)})
}

func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	item, err := s.store.GetItem(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	resp, ok := s.detailResponse(w, r, item)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePatchItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req patchItemRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid item patch")
		return
	}
	if req.Type == nil && req.Title == nil && req.Year == nil && req.Summary == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "no fields to update")
		return
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "title is required")
			return
		}
		req.Title = &title
	}
	if req.Type != nil {
		typ := strings.TrimSpace(*req.Type)
		switch typ {
		case "video", "movie", "episode":
			req.Type = &typ
		default:
			writeError(w, http.StatusBadRequest, "bad_request", "invalid item type")
			return
		}
	}
	if req.Summary != nil {
		summary := strings.TrimSpace(*req.Summary)
		req.Summary = &summary
	}

	item, err := s.store.UpdateItem(r.Context(), id, store.UpdateItemParams{
		Type:    req.Type,
		Title:   req.Title,
		Year:    req.Year,
		Summary: req.Summary,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.publishItemEvent(r, events.ItemUpdated, item.ID)
	resp, ok := s.detailResponse(w, r, item)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePutProgress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetItem(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	var req progressRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid progress")
		return
	}
	if req.PositionS < 0 || req.DurationS <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "position_s and duration_s must be valid")
		return
	}
	progress, err := s.store.UpsertProgress(r.Context(), id, req.PositionS, req.DurationS)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "saving progress failed")
		s.log.Error("save progress", "item_id", id, "error", err)
		return
	}
	s.publishItemEvent(r, events.ItemUpdated, id)
	writeJSON(w, http.StatusOK, progressToResponse(&progress))
}

func (s *Server) handleItemThumb(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetItem(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	files, err := s.store.ListFilesForItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing files failed")
		return
	}
	for _, file := range files {
		path := thumbs.Path(s.thumbsDir, file.ID)
		if _, err := os.Stat(path); err == nil {
			// Versioned requests (?v=<mtime>, from thumbURL) are immutable: a
			// regenerated thumb gets a new URL, so this cache entry never goes
			// stale. Unversioned requests (collections' thumb_urls) must
			// revalidate — ServeFile's Last-Modified makes those cheap 304s.
			if r.URL.Query().Get("v") != "" {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			w.Header().Set("Content-Type", "image/jpeg")
			http.ServeFile(w, r, path)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusRequestedRangeNotSatisfiable, "thumbnail_not_ready", "thumbnail has not been generated yet")
}

func itemToDetailResponse(item store.Item, collectionIDs []int64, progress *store.Progress, files []fileResponse) itemDetailResponse {
	return itemDetailResponse{
		ID:            item.ID,
		Type:          item.Type,
		Title:         item.Title,
		Year:          item.Year,
		Summary:       item.Summary,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
		DeletedAt:     item.DeletedAt,
		CollectionIDs: collectionIDs,
		Progress:      progressToResponse(progress),
		Files:         files,
	}
}

func (s *Server) detailResponse(w http.ResponseWriter, r *http.Request, item store.Item) (itemDetailResponse, bool) {
	summary, err := s.store.GetItemSummary(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "loading item summary failed")
		s.log.Error("get item summary", "item_id", item.ID, "error", err)
		return itemDetailResponse{}, false
	}
	progress, err := s.store.GetProgress(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "loading progress failed")
		s.log.Error("get item progress", "item_id", item.ID, "error", err)
		return itemDetailResponse{}, false
	}
	files, err := s.store.ListFilesForItem(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing files failed")
		s.log.Error("list item files", "item_id", item.ID, "error", err)
		return itemDetailResponse{}, false
	}
	fileResponses := make([]fileResponse, 0, len(files))
	for _, file := range files {
		streams, err := s.store.ListFileStreams(r.Context(), file.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "listing streams failed")
			s.log.Error("list streams", "file_id", file.ID, "error", err)
			return itemDetailResponse{}, false
		}
		fileResponses = append(fileResponses, fileToResponse(file, streams))
	}
	return itemToDetailResponse(item, summary.CollectionIDs, progress, fileResponses), true
}

func progressToResponse(progress *store.Progress) *progressResponse {
	if progress == nil {
		return nil
	}
	return &progressResponse{
		PositionS: progress.PositionS,
		Completed: progress.Completed,
	}
}

func listItemsOptsFromRequest(w http.ResponseWriter, r *http.Request) (store.ListItemsOpts, bool) {
	offset, ok := intQuery(w, r, "offset", 0)
	if !ok {
		return store.ListItemsOpts{}, false
	}
	limit, ok := intQuery(w, r, "limit", 60)
	if !ok {
		return store.ListItemsOpts{}, false
	}
	collectionID, ok := intQuery(w, r, "collection_id", 0)
	if !ok {
		return store.ListItemsOpts{}, false
	}
	return store.ListItemsOpts{
		Sort:         r.URL.Query().Get("sort"),
		Order:        r.URL.Query().Get("order"),
		Type:         r.URL.Query().Get("type"),
		CollectionID: int64(collectionID),
		Uncollected:  boolQuery(r, "uncollected"),
		Trashed:      boolQuery(r, "trashed"),
		InProgress:   boolQuery(r, "in_progress"),
		Offset:       offset,
		Limit:        limit,
	}, true
}

func boolQuery(r *http.Request, name string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func collectionIDQuery(r *http.Request) int {
	value, err := strconv.Atoi(r.URL.Query().Get("collection_id"))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (s *Server) itemSummaries(items []store.ItemSummary) []itemSummaryResponse {
	out := make([]itemSummaryResponse, 0, len(items))
	for _, item := range items {
		var progress *progressResponse
		if item.Progress != nil {
			progress = &progressResponse{
				PositionS: item.Progress.PositionS,
				Completed: item.Progress.Completed,
			}
		}
		out = append(out, itemSummaryResponse{
			ID:            item.ID,
			Type:          item.Type,
			Title:         item.Title,
			Year:          item.Year,
			DurationS:     item.DurationS,
			CreatedAt:     item.CreatedAt,
			ThumbURL:      s.thumbURL(item),
			Available:     item.Available,
			Progress:      progress,
			CollectionIDs: item.CollectionIDs,
		})
	}
	return out
}

// thumbURL appends ?v=<thumb mtime> once the thumbnail exists, which lets the
// handler serve versioned requests with Cache-Control: immutable. Re-probing a
// file rewrites the thumb (new mtime → new URL), and the version's first
// appearance — via the item.updated the thumbnail job publishes — is what
// swaps a card's 416 placeholder for the real image. The stat is against the
// local thumbs dir, never a media root.
func (s *Server) thumbURL(item store.ItemSummary) string {
	base := "/api/items/" + strconv.FormatInt(item.ID, 10) + "/thumb"
	for _, fileID := range item.FileIDs {
		if info, err := os.Stat(thumbs.Path(s.thumbsDir, fileID)); err == nil {
			return base + "?v=" + strconv.FormatInt(info.ModTime().UnixMilli(), 10)
		}
	}
	return base
}

func fileToResponse(file store.File, streams []store.Stream) fileResponse {
	resp := fileResponse{
		ID:        file.ID,
		RootID:    file.RootID,
		RelPath:   file.RelPath,
		Size:      file.Size,
		Container: file.Container,
		DurationS: file.DurationS,
		Width:     file.Width,
		Height:    file.Height,
		Bitrate:   file.Bitrate,
		Status:    file.Status,
		Streams:   make([]streamResponse, 0, len(streams)),
	}
	for _, stream := range streams {
		resp.Streams = append(resp.Streams, streamResponse{
			StreamIndex: stream.StreamIndex,
			Kind:        stream.Kind,
			Codec:       stream.Codec,
			Lang:        stream.Lang,
			Title:       stream.Title,
			Channels:    stream.Channels,
			IsDefault:   stream.IsDefault,
		})
	}
	return resp
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid id")
		return 0, false
	}
	return id, true
}

func intQuery(w http.ResponseWriter, r *http.Request, name string, fallback int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid "+name)
		return 0, false
	}
	return value, true
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if errors.Is(err, store.ErrInvalidInput) {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal server error")
}

func (s *Server) publishItemEvent(r *http.Request, eventType string, itemID int64) {
	if s.bus == nil {
		return
	}
	summary, err := s.store.GetItemSummary(r.Context(), itemID)
	if err != nil {
		s.log.Error("hydrate item event", "item_id", itemID, "error", err)
		return
	}
	s.bus.Publish(events.Event{Type: eventType, Payload: summary})
}
