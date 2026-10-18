package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/JeremyVun/MediaServer/internal/store"
)

type libraryResponse struct {
	Total   int   `json:"total"`
	Entries []any `json:"entries"` // itemEntry | showEntry
}

type itemEntry struct {
	Kind string `json:"kind"`
	itemSummaryResponse
}

type showEntry struct {
	Kind string `json:"kind"`
	showSummaryResponse
}

type showSummaryResponse struct {
	ID            int64           `json:"id"`
	Title         string          `json:"title"`
	Year          *int            `json:"year"`
	Network       *string         `json:"network"`
	ThumbURL      *string         `json:"thumb_url"`
	PosterURL     *string         `json:"poster_url"`
	BackdropURL   *string         `json:"backdrop_url"`
	CreatedAt     string          `json:"created_at"`
	LatestAddedAt string          `json:"latest_added_at"`
	SeasonCount   int             `json:"season_count"`
	EpisodeCount  int             `json:"episode_count"`
	Available     bool            `json:"available"`
	NeedsCheck    bool            `json:"needs_check"`
	HasProgress   bool            `json:"has_progress"`
	CollectionIDs []int64         `json:"collection_ids"`
	UpNext        *upNextResponse `json:"up_next"`
}

type upNextResponse struct {
	ItemID     int64             `json:"item_id"`
	Season     int               `json:"season"`
	Episode    int               `json:"episode"`
	EpisodeEnd *int              `json:"episode_end"`
	Title      string            `json:"title"`
	Available  bool              `json:"available"`
	DurationS  *float64          `json:"duration_s"`
	ThumbURL   string            `json:"thumb_url"`
	Progress   *progressResponse `json:"progress,omitempty"`
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	offset, ok := intQuery(w, r, "offset", 0)
	if !ok {
		return
	}
	limit, ok := intQuery(w, r, "limit", 60)
	if !ok {
		return
	}
	collectionID, ok := intQuery(w, r, "collection_id", 0)
	if !ok {
		return
	}
	q := r.URL.Query()
	entries, total, err := s.store.ListEntries(r.Context(), store.ListEntriesOpts{
		Sort:         q.Get("sort"),
		Order:        q.Get("order"),
		Type:         q.Get("type"),
		CollectionID: int64(collectionID),
		Uncollected:  boolQuery(r, "uncollected"),
		InProgress:   boolQuery(r, "in_progress"),
		Query:        q.Get("q"),
		Offset:       offset,
		Limit:        limit,
	})
	if errors.Is(err, store.ErrInvalidInput) {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing library failed")
		s.log.Error("list library", "error", err)
		return
	}

	var items []store.ItemSummary
	var shows []store.ShowSummary
	for _, e := range entries {
		if e.Kind == store.EntryShow {
			shows = append(shows, *e.Show)
		} else {
			items = append(items, *e.Item)
		}
	}
	showResponses, err := s.showSummaries(r.Context(), shows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing library failed")
		s.log.Error("list library shows", "error", err)
		return
	}
	itemResponses := s.itemSummaries(items)

	out := libraryResponse{Total: total, Entries: make([]any, 0, len(entries))}
	for _, e := range entries {
		if e.Kind == store.EntryShow {
			out.Entries = append(out.Entries, showEntry{Kind: store.EntryShow, showSummaryResponse: showResponses[0]})
			showResponses = showResponses[1:]
		} else {
			out.Entries = append(out.Entries, itemEntry{Kind: store.EntryItem, itemSummaryResponse: itemResponses[0]})
			itemResponses = itemResponses[1:]
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// showSummaries builds the wire shape in input order. thumb_url is the
// poster, else the up-next episode's thumbnail.
func (s *Server) showSummaries(ctx context.Context, shows []store.ShowSummary) ([]showSummaryResponse, error) {
	var upNextIDs []int64
	for _, sh := range shows {
		if sh.UpNext != nil {
			upNextIDs = append(upNextIDs, sh.UpNext.ItemID)
		}
	}
	fileIDs, err := s.store.ItemFileIDs(ctx, upNextIDs)
	if err != nil {
		return nil, err
	}
	out := make([]showSummaryResponse, len(shows))
	for i, sh := range shows {
		resp := showSummaryResponse{
			ID:            sh.ID,
			Title:         sh.Title,
			Year:          sh.Year,
			Network:       sh.Network,
			PosterURL:     s.showArtURL(sh.ID, "poster"),
			BackdropURL:   s.showArtURL(sh.ID, "backdrop"),
			CreatedAt:     sh.CreatedAt,
			LatestAddedAt: sh.LatestAddedAt,
			SeasonCount:   sh.SeasonCount,
			EpisodeCount:  sh.EpisodeCount,
			Available:     sh.Available,
			NeedsCheck:    sh.NeedsCheck,
			HasProgress:   sh.HasProgress,
			CollectionIDs: sh.CollectionIDs,
		}
		resp.ThumbURL = resp.PosterURL
		if up := sh.UpNext; up != nil {
			resp.UpNext = &upNextResponse{
				ItemID:     up.ItemID,
				Season:     up.Season,
				Episode:    up.Episode,
				EpisodeEnd: up.EpisodeEnd,
				Title:      up.Title,
				Available:  up.Available,
				DurationS:  up.DurationS,
				ThumbURL:   s.thumbURL(store.ItemSummary{ID: up.ItemID, FileIDs: fileIDs[up.ItemID]}),
				Progress:   progressToResponse(up.Progress),
			}
			if resp.ThumbURL == nil {
				resp.ThumbURL = &resp.UpNext.ThumbURL
			}
		}
		out[i] = resp
	}
	return out, nil
}

// showArtURL is the cached image's URL, versioned by its mtime like item
// thumbnails, or nil until the image is cached.
func (s *Server) showArtURL(showID int64, kind string) *string {
	info, err := os.Stat(s.showArtPath(showID, kind))
	if err != nil {
		return nil
	}
	u := "/api/shows/" + strconv.FormatInt(showID, 10) + "/" + kind + "?v=" + strconv.FormatInt(info.ModTime().UnixMilli(), 10)
	return &u
}

func (s *Server) showArtPath(showID int64, kind string) string {
	if s.classifier == nil {
		return ""
	}
	if kind == "poster" {
		return s.classifier.PosterPath(showID)
	}
	return s.classifier.BackdropPath(showID)
}
