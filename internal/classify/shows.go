package classify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

// ErrUnreachable marks a refresh that TVmaze could not answer; the job
// layer retries it after UnreachableRetry.
var ErrUnreachable = errors.New("tvmaze unreachable")

// PosterPath is where a show's cached TVmaze poster lives.
func (s *Service) PosterPath(showID int64) string {
	return filepath.Join(s.artDir, "shows", fmt.Sprintf("%d-poster.jpg", showID))
}

// BackdropPath is where a show's cached TVmaze background ("splash") lives.
func (s *Service) BackdropPath(showID int64) string {
	return filepath.Join(s.artDir, "shows", fmt.Sprintf("%d-backdrop.jpg", showID))
}

func (s *Service) removeArt(showID int64) {
	for _, p := range []string{s.PosterPath(showID), s.BackdropPath(showID)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("classify: remove show art", "path", p, "error", err)
		}
	}
}

// showRemoved finishes a show the store just deleted.
func (s *Service) showRemoved(show *store.Show) {
	if show == nil {
		return
	}
	s.removeArt(show.ID)
	s.publish(events.ShowRemoved, show.ID)
}

// RefreshShow runs the show_refresh job: refetch a matched show's metadata
// and episode list and retitle its unlocked episodes, or retry the match of
// a show formed while TVmaze was unreachable. An error wrapping
// ErrUnreachable asks for a retry later.
func (s *Service) RefreshShow(ctx context.Context, showID int64) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	show, err := s.store.GetShow(ctx, showID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if show.TVmazeID == nil {
		if show.RefreshedAt != nil || !show.NeedsCheck {
			return nil
		}
		return s.rematch(ctx, show)
	}
	if show, err = s.refreshMetadata(ctx, show); err != nil {
		return s.tvmazeFailure(ctx, show.ID, err)
	}
	return s.afterRefresh(ctx, show)
}

func (s *Service) afterRefresh(ctx context.Context, show store.Show) error {
	if err := s.retitle(ctx, show); err != nil {
		return err
	}
	s.publish(events.ShowUpdated, show.ID)
	return nil
}

// tvmazeError marks an error as TVmaze's rather than the store's.
type tvmazeError struct{ err error }

func (e tvmazeError) Error() string { return e.err.Error() }
func (e tvmazeError) Unwrap() error { return e.err }

func isTVmaze(err error) bool {
	var te tvmazeError
	return errors.As(err, &te)
}

func (s *Service) tvmazeFailure(ctx context.Context, showID int64, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case !isTVmaze(err):
		return err
	case errors.Is(err, tvmaze.ErrNotFound):
		s.log.Warn("classify: TVmaze no longer has this show", "show_id", showID, "error", err)
		return nil
	}
	return fmt.Errorf("refresh show %d: %w: %w", showID, ErrUnreachable, err)
}

// rematch retries matching an unmatched show formed while TVmaze was down.
// A weak result is never attached; a confident one whose show is already
// local merges this show into it, as classify would have attached there.
func (s *Service) rematch(ctx context.Context, show store.Show) error {
	q := showQuery{name: show.Title, key: library.ShowKey(show.Title), year: show.Year}
	items, err := s.store.ListItemsByShow(ctx, show.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Episode != nil {
			q.season, q.episode, _ = numbers(item)
			break
		}
	}
	m, err := s.match(ctx, q)
	if err != nil {
		return s.tvmazeFailure(ctx, show.ID, err)
	}
	if !m.confident {
		_, err := s.store.UpdateShow(ctx, show.ID, store.UpdateShowParams{RefreshedAt: store.SetTo(s.stamp())})
		return err
	}
	s.log.Info("classify: show matched on TVmaze after an outage", "show_id", show.ID, "tvmaze_id", m.result.ID)
	if _, err := s.adopt(ctx, show, m.result); err != nil {
		return s.tvmazeFailure(ctx, show.ID, err)
	}
	return nil
}

// adopt gives an unmatched show a confident TVmaze match: aliases are kept,
// metadata and episode titles refreshed. When another local show already
// has that match, show merges into it instead. It returns the show the
// items now belong to; a TVmaze error leaves the match set for a refresh.
func (s *Service) adopt(ctx context.Context, show store.Show, r tvmaze.Result) (store.Show, error) {
	id := int64(r.ID)
	local, err := s.store.GetShowByTVmazeID(ctx, id)
	if err == nil {
		s.log.Info("classify: matched show already local, merging", "show_id", show.ID, "into", local.ID)
		if err := s.merge(ctx, show.ID, local.ID); err != nil {
			return store.Show{}, err
		}
		return s.store.GetShow(ctx, local.ID)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Show{}, err
	}
	clear := false
	if show, err = s.store.UpdateShow(ctx, show.ID, store.UpdateShowParams{
		TVmazeID:   store.SetTo(id),
		NeedsCheck: &clear,
	}); err != nil {
		return store.Show{}, err
	}
	if show, err = s.refreshMetadata(ctx, show); err != nil {
		return show, err
	}
	return show, s.afterRefresh(ctx, show)
}

// refreshMetadata copies TVmaze's show, episode list and missing art into
// the catalog. A title_locked title is never changed.
func (s *Service) refreshMetadata(ctx context.Context, show store.Show) (store.Show, error) {
	tv, err := s.tvmaze.Show(ctx, int(*show.TVmazeID))
	if err != nil {
		return show, tvmazeError{err}
	}
	episodes := make([]store.ShowEpisode, 0, len(tv.Episodes))
	for _, e := range tv.Episodes {
		ep := store.ShowEpisode{Season: e.Season, Number: e.Number, Title: e.Title}
		if e.Airdate != "" {
			ep.Airdate = &e.Airdate
		}
		if e.RuntimeMin > 0 {
			ep.RuntimeMin = &e.RuntimeMin
		}
		episodes = append(episodes, ep)
	}
	if err := s.store.ReplaceShowEpisodes(ctx, show.ID, episodes); err != nil {
		return show, err
	}
	p := store.UpdateShowParams{RefreshedAt: store.SetTo(s.stamp())}
	if name := strings.TrimSpace(tv.Name); name != "" && !show.TitleLocked {
		p.Title = &name
	}
	if tv.Year != 0 {
		p.Year = store.SetTo(tv.Year)
	}
	if tv.Summary != "" {
		p.Summary = store.SetTo(tv.Summary)
	}
	if tv.Network != "" {
		p.Network = store.SetTo(tv.Network)
	}
	if show, err = s.store.UpdateShow(ctx, show.ID, p); err != nil {
		return show, err
	}
	return s.fetchArt(ctx, show, tv.PosterURL)
}

// maxBackgroundAttempts bounds how many dead background links are tried.
const maxBackgroundAttempts = 4

// fetchArt downloads whichever of the poster and backdrop is not cached
// yet, trying backgrounds best first past dead links (ruling B7). Art is
// best effort: a failure leaves it for the next refresh.
func (s *Service) fetchArt(ctx context.Context, show store.Show, posterURL string) (store.Show, error) {
	var p store.UpdateShowParams
	if show.PosterAt == nil && posterURL != "" {
		if err := s.cacheImage(ctx, posterURL, s.PosterPath(show.ID)); err != nil {
			s.log.Warn("classify: poster download failed", "show_id", show.ID, "error", err)
		} else {
			p.PosterAt = store.SetTo(s.stamp())
		}
	}
	if show.BackdropAt == nil {
		backgrounds, err := s.tvmaze.Backgrounds(ctx, int(*show.TVmazeID))
		if err != nil {
			s.log.Warn("classify: backdrop lookup failed", "show_id", show.ID, "error", err)
		}
		for _, bg := range backgrounds[:min(len(backgrounds), maxBackgroundAttempts)] {
			if err := s.cacheImage(ctx, bg.URL, s.BackdropPath(show.ID)); err != nil {
				s.log.Warn("classify: backdrop download failed", "show_id", show.ID, "error", err)
				if ctx.Err() != nil {
					break
				}
				continue
			}
			p.BackdropAt = store.SetTo(s.stamp())
			break
		}
	}
	if ctx.Err() != nil {
		return show, ctx.Err()
	}
	if !p.PosterAt.Set && !p.BackdropAt.Set {
		return show, nil
	}
	return s.store.UpdateShow(ctx, show.ID, p)
}

type episodeNumber struct{ season, number int }

func (s *Service) episodeTitles(ctx context.Context, showID int64) (map[episodeNumber]string, error) {
	eps, err := s.store.ListShowEpisodes(ctx, showID)
	if err != nil {
		return nil, err
	}
	titles := make(map[episodeNumber]string, len(eps))
	for _, e := range eps {
		titles[episodeNumber{e.Season, e.Number}] = strings.TrimSpace(e.Title)
	}
	return titles, nil
}

// maxRange bounds a multi-episode file's title lookups.
const maxRange = 50

// episodeTitle picks an episode's title: TVmaze's for (season + offset,
// episode), else the parsed episode title, else "Episode N". Specials are
// never offset.
func episodeTitle(titles map[episodeNumber]string, offset, season, episode, episodeEnd int, parsed string) string {
	tvSeason := season
	if season > 0 {
		tvSeason += offset
	}
	last := max(episode, episodeEnd)
	var found []string
	for n := episode; n <= last && n-episode < maxRange; n++ {
		t := titles[episodeNumber{tvSeason, n}]
		if t != "" && !slices.Contains(found, t) {
			found = append(found, t)
		}
	}
	if len(found) > 0 {
		return strings.Join(found, " / ")
	}
	return fallbackEpisodeTitle(parsed, episode, episodeEnd)
}

// retitle re-derives the title of every unlocked episode of a show.
func (s *Service) retitle(ctx context.Context, show store.Show) error {
	titles, err := s.episodeTitles(ctx, show.ID)
	if err != nil {
		return err
	}
	items, err := s.store.ListItemsByShow(ctx, show.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Locked || item.Type != "episode" || item.Episode == nil {
			continue
		}
		title, err := s.autoTitle(ctx, item, titles, show.SeasonOffset)
		if err != nil {
			return err
		}
		if title == item.Title {
			continue
		}
		if err := s.updateItem(ctx, item.ID, store.UpdateItemParams{Title: &title}); err != nil {
			return err
		}
	}
	return nil
}

// autoTitle is the title automation gives item at its current show
// position. The parsed episode title only counts when the parse agrees on
// the numbers.
func (s *Service) autoTitle(ctx context.Context, item store.Item, titles map[episodeNumber]string, offset int) (string, error) {
	relPath, _, err := s.fileOf(ctx, item.ID)
	if err != nil {
		return "", err
	}
	season, episode, end := numbers(item)
	parsed := library.ParsePath(relPath)
	parsedTitle := ""
	if parsed.Kind == library.KindEpisode && parsed.Season == season && parsed.Episode == episode {
		parsedTitle = parsed.EpisodeTitle
	}
	return episodeTitle(titles, offset, season, episode, end, parsedTitle), nil
}

func numbers(item store.Item) (season, episode, end int) {
	season = 1
	if item.Season != nil {
		season = *item.Season
	}
	if item.Episode != nil {
		episode = *item.Episode
	}
	if item.EpisodeEnd != nil {
		end = *item.EpisodeEnd
	}
	return season, episode, end
}

// AiringShowIDs lists matched shows whose latest aired TVmaze episode aired
// in the last 60 days, for the weekly refresh.
func (s *Service) AiringShowIDs(ctx context.Context) ([]int64, error) {
	today := s.now()
	return s.store.ListAiringShowIDs(ctx, today.AddDate(0, 0, -60).Format("2006-01-02"), today.Format("2006-01-02"))
}
