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

// collectionMember names exactly one item or one show.
type collectionMember struct {
	ItemID int64 `json:"item_id"`
	ShowID int64 `json:"show_id"`
}

func (m collectionMember) valid() bool { return (m.ItemID > 0) != (m.ShowID > 0) }

// reorderCollectionRequest takes entries in grid order, or the legacy
// item-only item_ids; exactly one of the two.
type reorderCollectionRequest struct {
	ItemIDs *[]int64            `json:"item_ids"`
	Entries *[]collectionMember `json:"entries"`
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
	var req collectionMember
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || !req.valid() {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid collection member: give item_id or show_id")
		return
	}
	if req.ShowID > 0 {
		if err := s.store.AddShowToCollection(r.Context(), id, req.ShowID); err != nil {
			writeStoreError(w, err)
			return
		}
		s.publishShow(events.ShowUpdated, req.ShowID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.store.AddItemToCollection(r.Context(), id, req.ItemID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.publishItemChange(r, req.ItemID)
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
	s.publishItemChange(r, itemID)
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveCollectionShow removes the show and its episodes from the
// collection (ruling B3), since either one keeps the show a member.
func (s *Server) handleRemoveCollectionShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	showID, ok := pathID(w, r, "showId")
	if !ok {
		return
	}
	episodes, err := s.store.RemoveShowFromCollection(r.Context(), id, showID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, itemID := range episodes {
		s.publishItemEvent(r, events.ItemUpdated, itemID)
	}
	s.publishShow(events.ShowUpdated, showID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req reorderCollectionRequest
	if !decodeStrict(w, r, &req, "invalid collection order") {
		return
	}
	if (req.ItemIDs == nil) == (req.Entries == nil) {
		writeError(w, http.StatusBadRequest, "bad_request", "give either entries or item_ids")
		return
	}
	var members []store.CollectionRef
	if req.ItemIDs != nil {
		for _, itemID := range *req.ItemIDs {
			members = append(members, store.CollectionRef{ItemID: itemID})
		}
	} else {
		for _, m := range *req.Entries {
			if !m.valid() {
				writeError(w, http.StatusBadRequest, "bad_request", "each entry names one item_id or one show_id")
				return
			}
			members = append(members, store.CollectionRef{ItemID: m.ItemID, ShowID: m.ShowID})
		}
	}
	if err := s.store.ReorderCollection(r.Context(), id, members); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publishItemChange sends item.updated, and show.updated for an episode,
// whose show's collection_ids follow its episodes (ruling B3).
func (s *Server) publishItemChange(r *http.Request, itemID int64) {
	s.publishItemEvent(r, events.ItemUpdated, itemID)
	if item, err := s.store.GetItem(r.Context(), itemID); err == nil {
		s.publishShowOf(item)
	}
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
