package httpapi

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/classify"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

type showDetailResponse struct {
	showSummaryResponse
	Summary      *string          `json:"summary"`
	TVmazeID     *int64           `json:"tvmaze_id"`
	SeasonOffset int              `json:"season_offset"`
	TitleLocked  bool             `json:"title_locked"`
	Seasons      []seasonResponse `json:"seasons"`
}

type seasonResponse struct {
	Season   int   `json:"season"`
	Episodes []any `json:"episodes"` // itemEntry | missingEpisodeResponse
}

// missingEpisodeResponse is a TVmaze episode with no file, aired or not:
// the client tells unaired ones by a future airdate.
type missingEpisodeResponse struct {
	Kind    string  `json:"kind"`
	Season  int     `json:"season"`
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	Airdate *string `json:"airdate"`
	Missing bool    `json:"missing"`
}

type patchShowRequest struct {
	Title     *string `json:"title"`
	Confirmed bool    `json:"confirmed"`
}

type showMatchRequest struct {
	TVmazeID     optional[int64] `json:"tvmaze_id"`
	SeasonOffset *int            `json:"season_offset"`
}

type showMergeRequest struct {
	IntoShowID int64 `json:"into_show_id"`
}

type matchCandidateResponse struct {
	TVmazeID    int64   `json:"tvmaze_id"`
	Name        string  `json:"name"`
	Year        *int    `json:"year"`
	Network     *string `json:"network"`
	PosterURL   *string `json:"poster_url"`
	LocalShowID *int64  `json:"local_show_id"`
}

type matchTakenResponse struct {
	Error  apiError `json:"error"`
	ShowID int64    `json:"show_id"`
}

func (s *Server) handleGetShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	s.writeShowDetail(w, r, id)
}

func (s *Server) handlePatchShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req patchShowRequest
	if !decodeStrict(w, r, &req, "invalid show patch") {
		return
	}
	if _, err := s.classifier.EditShow(r.Context(), id, classify.ShowEdit{Title: req.Title, Confirmed: req.Confirmed}); err != nil {
		s.writeServiceError(w, "edit show", err)
		return
	}
	s.writeShowDetail(w, r, id)
}

func (s *Server) handleShowMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req showMatchRequest
	if !decodeStrict(w, r, &req, "invalid show match") {
		return
	}
	if !req.TVmazeID.Set {
		writeError(w, http.StatusBadRequest, "bad_request", "tvmaze_id is required (null for not on TVmaze)")
		return
	}
	if _, err := s.classifier.ChangeMatch(r.Context(), id, classify.Match{
		TVmazeID: req.TVmazeID.Value, SeasonOffset: req.SeasonOffset,
	}); err != nil {
		s.writeServiceError(w, "change show match", err)
		return
	}
	s.writeShowDetail(w, r, id)
}

func (s *Server) handleShowMerge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req showMergeRequest
	if !decodeStrict(w, r, &req, "invalid show merge") {
		return
	}
	if req.IntoShowID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "into_show_id is required")
		return
	}
	if _, err := s.classifier.MergeShows(r.Context(), id, req.IntoShowID); err != nil {
		s.writeServiceError(w, "merge shows", err)
		return
	}
	s.writeShowDetail(w, r, req.IntoShowID)
}

func (s *Server) handleShowMatchSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "q is required")
		return
	}
	if s.tvmaze == nil {
		writeError(w, http.StatusServiceUnavailable, "tvmaze_unavailable", "TVmaze is not configured")
		return
	}
	results, err := s.tvmaze.Search(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tvmaze_unavailable", "TVmaze could not be reached")
		s.log.Warn("tvmaze match search", "q", q, "error", err)
		return
	}
	out := make([]matchCandidateResponse, 0, len(results))
	for _, res := range results {
		c := matchCandidateResponse{
			TVmazeID:  int64(res.ID),
			Name:      res.Name,
			Network:   nonEmptyString(res.Network),
			PosterURL: nonEmptyString(cmp.Or(res.PosterMediumURL, res.PosterURL)),
		}
		if res.Year > 0 {
			c.Year = &res.Year
		}
		local, err := s.store.GetShowByTVmazeID(r.Context(), c.TVmazeID)
		switch {
		case err == nil:
			c.LocalShowID = &local.ID
		case !errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusInternalServerError, "internal", "looking up local shows failed")
			s.log.Error("match search local show", "tvmaze_id", c.TVmazeID, "error", err)
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *Server) handleShowMatchSeasons(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("tvmaze_id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "tvmaze_id must be a positive integer")
		return
	}
	if s.tvmaze == nil {
		writeError(w, http.StatusServiceUnavailable, "tvmaze_unavailable", "TVmaze is not configured")
		return
	}
	seasons, err := s.tvmaze.Seasons(r.Context(), id)
	switch {
	case errors.Is(err, tvmaze.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such show on TVmaze")
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, "tvmaze_unavailable", "TVmaze could not be reached")
		s.log.Warn("tvmaze match seasons", "tvmaze_id", id, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seasons": seasons})
}

func (s *Server) handleShowPoster(w http.ResponseWriter, r *http.Request) {
	s.serveShowArt(w, r, "poster")
}

func (s *Server) handleShowBackdrop(w http.ResponseWriter, r *http.Request) {
	s.serveShowArt(w, r, "backdrop")
}

func (s *Server) serveShowArt(w http.ResponseWriter, r *http.Request, kind string) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	path := s.showArtPath(id, kind)
	if info, err := os.Stat(path); path == "" || err != nil || !info.Mode().IsRegular() {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusNotFound, "not_found", "no cached "+kind+" for this show")
		return
	}
	// Same policy as item thumbnails: a versioned URL changes when the file does.
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// handleTrashShow trashes every live episode, all or nothing: any episode
// that cannot move to its root's .trash fails the whole request untouched.
func (s *Server) handleTrashShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetShow(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	items, err := s.store.ListItemsByShow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing episodes failed")
		s.log.Error("list show episodes for trash", "show_id", id, "error", err)
		return
	}
	var plans []trashPlan
	ids := []int64{}
	for _, item := range items {
		if item.DeletedAt != nil {
			continue
		}
		itemPlans, ok := s.trashPlans(w, r, item, false)
		if !ok {
			return
		}
		plans = append(plans, itemPlans...)
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		writeError(w, http.StatusConflict, "already_trashed", "show has no episodes outside the trash")
		return
	}
	moved, ok := s.moveToTrash(w, plans)
	if !ok {
		return
	}
	if err := s.store.MarkItemsTrashed(r.Context(), ids); err != nil {
		rollbackMoves(moved)
		writeStoreError(w, err)
		return
	}
	for _, itemID := range ids {
		s.publishRemoved(itemID)
	}
	s.publishShow(events.ShowUpdated, id)
	writeJSON(w, http.StatusOK, map[string]any{"item_ids": ids})
}

func (s *Server) writeShowDetail(w http.ResponseWriter, r *http.Request, id int64) {
	resp, err := s.showDetail(r, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "show not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "loading show failed")
		s.log.Error("load show detail", "show_id", id, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) showDetail(r *http.Request, id int64) (showDetailResponse, error) {
	ctx := r.Context()
	show, err := s.store.GetShow(ctx, id)
	if err != nil {
		return showDetailResponse{}, err
	}
	summary, err := s.store.GetShowSummary(ctx, id)
	if err != nil {
		return showDetailResponse{}, err
	}
	summaries, err := s.showSummaries(ctx, []store.ShowSummary{summary})
	if err != nil {
		return showDetailResponse{}, err
	}
	items, err := s.store.ListShowItems(ctx, id)
	if err != nil {
		return showDetailResponse{}, err
	}
	tvEpisodes, err := s.store.ListShowEpisodes(ctx, id)
	if err != nil {
		return showDetailResponse{}, err
	}
	return showDetailResponse{
		showSummaryResponse: summaries[0],
		Summary:             show.Summary,
		TVmazeID:            show.TVmazeID,
		SeasonOffset:        show.SeasonOffset,
		TitleLocked:         show.TitleLocked,
		Seasons:             seasons(s.itemSummaries(items), tvEpisodes, show.SeasonOffset),
	}, nil
}

// maxEpisodeSpan bounds how many numbers one multi-episode file covers.
const maxEpisodeSpan = 50

type seasonEntry struct {
	number int
	value  any
}

// seasons lays the show out in file numbering: TVmaze season S holds file
// season S - offset (specials are never offset), and a TVmaze episode no
// live file covers is missing. TVmaze seasons that land before file season
// 1 have no file counterpart and are left out. Specials come last.
func seasons(items []itemSummaryResponse, tvEpisodes []store.ShowEpisode, offset int) []seasonResponse {
	type slot struct{ season, number int }
	covered := map[slot]bool{}
	bySeason := map[int][]seasonEntry{}
	for _, it := range items {
		season, number := 1, 0
		if it.Season != nil {
			season = *it.Season
		}
		if it.Episode != nil {
			number = *it.Episode
			last := number
			if it.EpisodeEnd != nil {
				last = max(last, *it.EpisodeEnd)
			}
			for n := number; n <= last && n-number < maxEpisodeSpan; n++ {
				covered[slot{season, n}] = true
			}
		}
		bySeason[season] = append(bySeason[season], seasonEntry{number, itemEntry{Kind: store.EntryItem, itemSummaryResponse: it}})
	}
	for _, e := range tvEpisodes {
		season := e.Season
		if season > 0 {
			season -= offset
			if season <= 0 {
				continue
			}
		}
		if covered[slot{season, e.Number}] {
			continue
		}
		bySeason[season] = append(bySeason[season], seasonEntry{e.Number, missingEpisodeResponse{
			Kind: "missing", Season: season, Number: e.Number, Title: e.Title, Airdate: e.Airdate, Missing: true,
		}})
	}

	numbers := make([]int, 0, len(bySeason))
	for n := range bySeason {
		numbers = append(numbers, n)
	}
	slices.SortFunc(numbers, func(a, b int) int {
		if (a == 0) != (b == 0) {
			if a == 0 {
				return 1
			}
			return -1
		}
		return cmp.Compare(a, b)
	})
	out := make([]seasonResponse, 0, len(numbers))
	for _, n := range numbers {
		entries := bySeason[n]
		slices.SortStableFunc(entries, func(a, b seasonEntry) int { return cmp.Compare(a.number, b.number) })
		values := make([]any, len(entries))
		for i, e := range entries {
			values[i] = e.value
		}
		out = append(out, seasonResponse{Season: n, Episodes: values})
	}
	return out
}

// writeServiceError maps classify and store errors onto the envelope.
func (s *Server) writeServiceError(w http.ResponseWriter, op string, err error) {
	var taken *classify.MatchTakenError
	switch {
	case errors.As(err, &taken):
		writeJSON(w, http.StatusConflict, matchTakenResponse{
			Error:  apiError{Code: "match_taken", Message: "another show already has this TVmaze match; merge into it instead"},
			ShowID: taken.ShowID,
		})
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalidInput), errors.Is(err, store.ErrConflict):
		writeStoreError(w, err)
	default:
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
		s.log.Error(op, "error", err)
	}
}

func (s *Server) publishShow(eventType string, showID int64) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: eventType, Payload: map[string]any{"id": showID}})
	}
}

// publishShowOf tells clients an episode's show changed with it (its up
// next, counts or collections).
func (s *Server) publishShowOf(item store.Item) {
	if item.ShowID != nil {
		s.publishShow(events.ShowUpdated, *item.ShowID)
	}
}

func decodeStrict(w http.ResponseWriter, r *http.Request, v any, message string) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", message)
		return false
	}
	return true
}

// optional tells an absent JSON field (Set false) from an explicit null
// (Set, Value nil).
type optional[T any] store.Nullable[T]

func (o *optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

func nonEmptyString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
