package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
)

type collectionResponse struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	ItemCount int      `json:"item_count"`
	ThumbURLs []string `json:"thumb_urls"`
}

type collectionNameRequest struct {
	Name string `json:"name"`
}

type addCollectionItemRequest struct {
	ItemID int64 `json:"item_id"`
}

type reorderCollectionRequest struct {
	ItemIDs []int64 `json:"item_ids"`
}

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	collections, err := s.store.ListCollections(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing collections failed")
		s.log.Error("list collections", "error", err)
		return
	}
	out := make([]collectionResponse, 0, len(collections))
	for _, collection := range collections {
		out = append(out, collectionToResponse(collection))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var req collectionNameRequest
	if !decodeCollectionName(w, r, &req) {
		return
	}
	collection, err := s.store.CreateCollection(r.Context(), req.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, collectionToResponse(store.CollectionSummary{Collection: collection}))
}

func (s *Server) handlePatchCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req collectionNameRequest
	if !decodeCollectionName(w, r, &req) {
		return
	}
	collection, err := s.store.UpdateCollection(r.Context(), id, req.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, collectionToResponse(store.CollectionSummary{Collection: collection}))
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.store.DeleteCollection(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddCollectionItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req addCollectionItemRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.ItemID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid collection item")
		return
	}
	if err := s.store.AddItemToCollection(r.Context(), id, req.ItemID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.publishItemEvent(r, events.ItemUpdated, req.ItemID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveCollectionItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	itemID, ok := pathID(w, r, "itemId")
	if !ok {
		return
	}
	if err := s.store.RemoveItemFromCollection(r.Context(), id, itemID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.publishItemEvent(r, events.ItemUpdated, itemID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req reorderCollectionRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid collection order")
		return
	}
	if err := s.store.ReorderCollection(r.Context(), id, req.ItemIDs); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeCollectionName(w http.ResponseWriter, r *http.Request, req *collectionNameRequest) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid collection")
		return false
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "collection name is required")
		return false
	}
	return true
}

func collectionToResponse(collection store.CollectionSummary) collectionResponse {
	thumbURLs := make([]string, 0, len(collection.ThumbItemIDs))
	for _, id := range collection.ThumbItemIDs {
		thumbURLs = append(thumbURLs, "/api/items/"+strconv.FormatInt(id, 10)+"/thumb")
	}
	return collectionResponse{
		ID:        collection.ID,
		Name:      collection.Name,
		ItemCount: collection.ItemCount,
		ThumbURLs: thumbURLs,
	}
}
