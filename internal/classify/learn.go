package classify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
)

// ErrEpisodeNeedsShow rejects making an item an episode without naming its
// show (the API's invalid_episode).
var ErrEpisodeNeedsShow = fmt.Errorf("an episode needs a show: %w", store.ErrInvalidInput)

// MatchTakenError rejects a TVmaze match another local show already has;
// merging into that show is the way to combine them.
type MatchTakenError struct{ ShowID int64 }

func (e *MatchTakenError) Error() string {
	return fmt.Sprintf("tvmaze show already belongs to show %d", e.ShowID)
}

func (e *MatchTakenError) Unwrap() error { return store.ErrConflict }

// ItemEdit is an owner edit of one item; nil fields are left alone.
type ItemEdit struct {
	Title      *string
	Type       *string // video|movie|episode
	Year       *int
	Summary    *string
	ShowID     *int64 // move the item to this show (makes it an episode)
	Season     *int
	Episode    *int
	EpisodeEnd store.Nullable[int]
	Confirmed  bool // "Looks right"
}

// EditItem applies an owner edit per design "Learning from edits": moving
// to a show teaches the owner alias item.show_key → show; editing title,
// type, show, season or episode locks the item and clears its Check mark;
// leaving episode detaches it from its show. Bad values are
// store.ErrInvalidInput (ErrEpisodeNeedsShow among them); a missing item is
// store.ErrNotFound.
func (s *Service) EditItem(ctx context.Context, itemID int64, e ItemEdit) (store.Item, error) {
	if err := s.lock(ctx); err != nil {
		return store.Item{}, err
	}
	defer s.unlock()
	identity := e.Title != nil || e.Type != nil || e.ShowID != nil || e.Season != nil || e.Episode != nil || e.EpisodeEnd.Set
	if !identity && !e.Confirmed && e.Year == nil && e.Summary == nil {
		return store.Item{}, fmt.Errorf("nothing to change: %w", store.ErrInvalidInput)
	}
	item, err := s.store.GetItem(ctx, itemID)
	if err != nil {
		return store.Item{}, err
	}
	if e.Title != nil {
		title := strings.TrimSpace(*e.Title)
		if title == "" {
			return store.Item{}, fmt.Errorf("title is required: %w", store.ErrInvalidInput)
		}
		e.Title = &title
	}

	typ := item.Type
	if e.Type != nil {
		switch *e.Type {
		case "video", "movie", "episode":
			typ = *e.Type
		default:
			return store.Item{}, fmt.Errorf("bad item type %q: %w", *e.Type, store.ErrInvalidInput)
		}
	}
	var target *store.Show
	if e.ShowID != nil {
		if e.Type != nil && *e.Type != "episode" {
			return store.Item{}, fmt.Errorf("only an episode belongs to a show: %w", store.ErrInvalidInput)
		}
		show, err := s.store.GetShow(ctx, *e.ShowID)
		if errors.Is(err, store.ErrNotFound) {
			return store.Item{}, fmt.Errorf("show %d does not exist: %w", *e.ShowID, store.ErrInvalidInput)
		}
		if err != nil {
			return store.Item{}, err
		}
		target, typ = &show, "episode"
	}
	if e.Type != nil && *e.Type == "episode" && item.ShowID == nil && target == nil {
		return store.Item{}, ErrEpisodeNeedsShow
	}
	if (e.Season != nil || e.Episode != nil || e.EpisodeEnd.Set) && typ != "episode" {
		return store.Item{}, fmt.Errorf("season and episode belong to episodes: %w", store.ErrInvalidInput)
	}
	if err := validNumbers(item, e); err != nil {
		return store.Item{}, err
	}

	p := store.UpdateItemParams{Title: e.Title}
	if e.Year != nil {
		p.Year = store.SetTo(*e.Year)
	}
	if e.Summary != nil {
		owned := true
		p.Summary, p.SummaryLocked = store.SetTo(*e.Summary), &owned
	}
	if identity {
		locked := true
		p.Locked = &locked
	}
	if identity || e.Confirmed {
		clear := false
		p.NeedsCheck = &clear
	}
	if typ != item.Type {
		p.Type = &typ
	}
	leftMovie := item.Type == "movie" && typ != "movie"
	if leftMovie {
		// TMDB data describes a movie; a later return to movie matches afresh.
		p.TMDBID, p.ArtAt, p.TMDBCheckedAt = store.SetNull[int64](), store.SetNull[string](), store.SetNull[string]()
		if !item.SummaryLocked && e.Summary == nil {
			p.Summary = store.SetNull[string]()
		}
	}

	oldShow := item.ShowID
	newShow := oldShow
	switch {
	case typ != "episode":
		newShow = nil
		p.ShowID = store.SetNull[int64]()
		p.Season, p.Episode, p.EpisodeEnd = store.SetNull[int](), store.SetNull[int](), store.SetNull[int]()
	case target != nil:
		newShow = &target.ID
		p.ShowID = store.SetTo(target.ID)
	}
	if typ == "episode" {
		if e.Season != nil {
			p.Season = store.SetTo(*e.Season)
		}
		if e.Episode != nil {
			p.Episode = store.SetTo(*e.Episode)
		}
		if e.EpisodeEnd.Set {
			p.EpisodeEnd = e.EpisodeEnd
		}
	}

	relPath, _, err := s.fileOf(ctx, item.ID)
	if err != nil {
		return store.Item{}, err
	}
	key := derefOr(item.ShowKey, library.ParsePath(relPath).ShowKey)
	if e.Title == nil && typ == "episode" && newShow != nil {
		moved := after(item, p)
		if !sameShow(oldShow, newShow) || !samePosition(item, moved) {
			if title, follows, err := s.titleFollows(ctx, item, moved, oldShow, *newShow); err != nil {
				return store.Item{}, err
			} else if follows {
				p.Title = &title
			}
		}
	}
	if item.ClassifiedAt == nil && identity && (typ != "episode" || newShow != nil) {
		p.ClassifiedAt = store.SetTo(s.stamp())
	}

	updated, err := s.store.UpdateItem(ctx, item.ID, p)
	if err != nil {
		return store.Item{}, err
	}
	if target != nil && key != "" {
		if _, err := s.store.PutShowAlias(ctx, key, target.ID, store.AliasOwner); err != nil {
			return store.Item{}, err
		}
	}
	if leftMovie {
		s.removeItemArt(item.ID)
	}
	s.publishItem(ctx, item.ID)
	if typ == "movie" && item.Type != "movie" && updated.ClassifiedAt != nil && s.TMDBConfigured() {
		s.scheduleMovieRefresh(ctx, item.ID, s.now())
	}
	if oldShow != nil && !sameShow(oldShow, newShow) {
		if err := s.leftShow(ctx, *oldShow); err != nil {
			return store.Item{}, err
		}
	}
	if newShow != nil {
		s.publish(events.ShowUpdated, *newShow)
	}
	return updated, nil
}

// MoveEpisode files an item under a show (the show picker).
func (s *Service) MoveEpisode(ctx context.Context, itemID, showID int64) (store.Item, error) {
	return s.EditItem(ctx, itemID, ItemEdit{ShowID: &showID})
}

// ConfirmItem is "Looks right" on an item: it clears the Check mark.
func (s *Service) ConfirmItem(ctx context.Context, itemID int64) (store.Item, error) {
	return s.EditItem(ctx, itemID, ItemEdit{Confirmed: true})
}

func validNumbers(item store.Item, e ItemEdit) error {
	if e.Season != nil && *e.Season < 0 {
		return fmt.Errorf("season must be 0 or more: %w", store.ErrInvalidInput)
	}
	if e.Episode != nil && *e.Episode < 1 {
		return fmt.Errorf("episode must be 1 or more: %w", store.ErrInvalidInput)
	}
	episode := item.Episode
	if e.Episode != nil {
		episode = e.Episode
	}
	if end := e.EpisodeEnd.Value; end != nil && (episode == nil || *end <= *episode) {
		return fmt.Errorf("episode_end must come after episode: %w", store.ErrInvalidInput)
	}
	return nil
}

// after is item as the edit p leaves it, for the numbers only.
func after(item store.Item, p store.UpdateItemParams) store.Item {
	apply := func(cur *int, n store.Nullable[int]) *int {
		if n.Set {
			return n.Value
		}
		return cur
	}
	item.Season = apply(item.Season, p.Season)
	item.Episode = apply(item.Episode, p.Episode)
	item.EpisodeEnd = apply(item.EpisodeEnd, p.EpisodeEnd)
	return item
}

func samePosition(a, b store.Item) bool {
	as, ae, aend := numbers(a)
	bs, be, bend := numbers(b)
	return as == bs && ae == be && aend == bend
}

func sameShow(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// titleFollows decides whether a moved or renumbered episode takes the
// title of its new position: only when its current title is the one
// automation gave it, so an owner's own title is never replaced.
func (s *Service) titleFollows(ctx context.Context, before, moved store.Item, oldShow *int64, newShow int64) (string, bool, error) {
	if moved.Episode == nil {
		return "", false, nil
	}
	current, err := s.automaticTitle(ctx, before, oldShow)
	if err != nil || current != before.Title {
		return "", false, err
	}
	title, err := s.automaticTitle(ctx, moved, &newShow)
	return title, err == nil, err
}

// automaticTitle is the title automation gives item at its position in
// showID (nil: no show, so no TVmaze titles).
func (s *Service) automaticTitle(ctx context.Context, item store.Item, showID *int64) (string, error) {
	if item.Episode == nil {
		relPath, _, err := s.fileOf(ctx, item.ID)
		return Guess(relPath, 0).Title, err
	}
	titles := map[episodeNumber]string{}
	offset := 0
	if showID != nil {
		show, err := s.store.GetShow(ctx, *showID)
		if err != nil {
			return "", err
		}
		if titles, err = s.episodeTitles(ctx, show.ID); err != nil {
			return "", err
		}
		offset = show.SeasonOffset
	}
	return s.autoTitle(ctx, item, titles, offset)
}

// leftShow tidies a show an item just left: it is deleted once nothing
// belongs to it, otherwise it changed.
func (s *Service) leftShow(ctx context.Context, showID int64) error {
	removed, err := s.store.DeleteShowIfEmpty(ctx, showID)
	if err != nil {
		return err
	}
	if removed != nil {
		s.showRemoved(removed)
		return nil
	}
	s.publish(events.ShowUpdated, showID)
	return nil
}

// ShowEdit is an owner edit of a show; Title renames it and locks the
// title against TVmaze, Confirmed is "Looks right".
type ShowEdit struct {
	Title     *string
	Confirmed bool
}

// EditShow renames and/or confirms a show. Aliases are unchanged.
func (s *Service) EditShow(ctx context.Context, showID int64, e ShowEdit) (store.Show, error) {
	if e.Title == nil && !e.Confirmed {
		return store.Show{}, fmt.Errorf("nothing to change: %w", store.ErrInvalidInput)
	}
	if err := s.lock(ctx); err != nil {
		return store.Show{}, err
	}
	defer s.unlock()
	var p store.UpdateShowParams
	if e.Title != nil {
		locked := true
		p.Title, p.TitleLocked = e.Title, &locked
	}
	if e.Confirmed {
		clear := false
		p.NeedsCheck = &clear
	}
	show, err := s.store.UpdateShow(ctx, showID, p)
	if err != nil {
		return store.Show{}, err
	}
	s.publish(events.ShowUpdated, show.ID)
	return show, nil
}

// RenameShow sets the show's title for good (title_locked).
func (s *Service) RenameShow(ctx context.Context, showID int64, title string) (store.Show, error) {
	return s.EditShow(ctx, showID, ShowEdit{Title: &title})
}

// ConfirmShow is "Looks right" on a show.
func (s *Service) ConfirmShow(ctx context.Context, showID int64) (store.Show, error) {
	return s.EditShow(ctx, showID, ShowEdit{Confirmed: true})
}

// MergeShows moves every item of from into into, repoints from's aliases
// so later arrivals follow, deletes from and its art, and retitles the
// unlocked episodes by into's episode list. It returns into.
func (s *Service) MergeShows(ctx context.Context, from, into int64) (store.Show, error) {
	if err := s.lock(ctx); err != nil {
		return store.Show{}, err
	}
	defer s.unlock()
	if err := s.merge(ctx, from, into); err != nil {
		return store.Show{}, err
	}
	return s.store.GetShow(ctx, into)
}

func (s *Service) merge(ctx context.Context, from, into int64) error {
	moved, err := s.store.ListItemsByShow(ctx, from)
	if err != nil {
		return err
	}
	removed, err := s.store.MergeShow(ctx, from, into)
	if err != nil {
		return err
	}
	s.showRemoved(&removed)
	show, err := s.store.GetShow(ctx, into)
	if err != nil {
		return err
	}
	if err := s.retitle(ctx, show); err != nil {
		return err
	}
	for _, item := range moved {
		s.publishItem(ctx, item.ID)
	}
	s.publish(events.ShowUpdated, into)
	return nil
}

// Match is a Change dialog choice: a TVmaze show (nil = "Not on TVmaze")
// and optionally the season offset (TVmaze season = file season + offset).
type Match struct {
	TVmazeID     *int64
	SeasonOffset *int
}

// ChangeMatch re-points a show at another TVmaze show, or at none, and
// clears its Check mark. Aliases are unchanged. A new match drops the old
// episode list and art and queues a show_refresh to fetch the new ones; a
// TVmaze show another local show has is a *MatchTakenError.
func (s *Service) ChangeMatch(ctx context.Context, showID int64, m Match) (store.Show, error) {
	if m.TVmazeID != nil && *m.TVmazeID <= 0 {
		return store.Show{}, fmt.Errorf("bad tvmaze id: %w", store.ErrInvalidInput)
	}
	if err := s.lock(ctx); err != nil {
		return store.Show{}, err
	}
	defer s.unlock()
	show, err := s.store.GetShow(ctx, showID)
	if err != nil {
		return store.Show{}, err
	}
	if m.TVmazeID != nil {
		other, err := s.store.GetShowByTVmazeID(ctx, *m.TVmazeID)
		if err == nil && other.ID != showID {
			return store.Show{}, &MatchTakenError{ShowID: other.ID}
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return store.Show{}, err
		}
	}
	same := m.TVmazeID != nil && show.TVmazeID != nil && *show.TVmazeID == *m.TVmazeID
	offset := 0
	if same {
		offset = show.SeasonOffset
	}
	if m.SeasonOffset != nil {
		offset = *m.SeasonOffset
	}
	clear := false
	p := store.UpdateShowParams{SeasonOffset: &offset, NeedsCheck: &clear}
	if !same {
		s.removeArt(show.ID)
		if err := s.store.ReplaceShowEpisodes(ctx, show.ID, nil); err != nil {
			return store.Show{}, err
		}
		p.PosterAt, p.BackdropAt = store.SetNull[string](), store.SetNull[string]()
		if m.TVmazeID != nil {
			p.TVmazeID = store.SetTo(*m.TVmazeID)
			p.RefreshedAt = store.SetNull[string]()
		} else {
			// Settled as unmatched, so a pending refresh never re-matches it.
			p.TVmazeID = store.SetNull[int64]()
			p.Summary = store.SetNull[string]()
			p.RefreshedAt = store.SetTo(s.stamp())
		}
	}
	if show, err = s.store.UpdateShow(ctx, show.ID, p); err != nil {
		return store.Show{}, err
	}
	if err := s.retitle(ctx, show); err != nil {
		return store.Show{}, err
	}
	if m.TVmazeID != nil && !same {
		s.scheduleRefresh(ctx, show.ID, s.now())
	}
	s.publish(events.ShowUpdated, show.ID)
	return show, nil
}

// HardDeleteItem purges an item's row and its cached backdrop. When it was
// its show's last item the show goes too, with its cached art, and
// show.removed is published.
// Publishing item.removed stays with the caller.
func (s *Service) HardDeleteItem(ctx context.Context, itemID int64) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	removed, err := s.store.HardDeleteItem(ctx, itemID)
	if err != nil {
		return err
	}
	s.removeItemArt(itemID)
	s.showRemoved(removed)
	return nil
}
