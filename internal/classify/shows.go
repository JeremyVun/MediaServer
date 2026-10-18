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
// ErrUnreachable asks for a retry later. TVmaze is asked before taking the
// lock, and the show is re-read under it.
func (s *Service) RefreshShow(ctx context.Context, showID int64) error {
	for attempt := 0; ; attempt++ {
		art, err := s.refreshOnce(ctx, showID)
		if !errors.As(err, &staleLookup{}) || attempt == maxStale {
			s.fetchArt(ctx, art)
			return err
		}
	}
}

func (s *Service) refreshOnce(ctx context.Context, showID int64) (*artJob, error) {
	show, err := s.store.GetShow(ctx, showID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if show.TVmazeID == nil {
		if awaiting, err := s.awaitingRematch(ctx, show); err != nil || !awaiting {
			return nil, err
		}
		q, err := s.rematchQuery(ctx, show)
		if err != nil {
			return nil, err
		}
		lk, err := s.lookUp(ctx, q)
		if err != nil {
			return nil, err
		}
		return s.locked(ctx, func() (*artJob, error) { return s.rematch(ctx, showID, lk) })
	}
	tvmazeID := *show.TVmazeID
	tv, fetchErr := s.tvmaze.Show(ctx, int(tvmazeID))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return s.locked(ctx, func() (*artJob, error) {
		show, err := s.store.GetShow(ctx, showID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if show.TVmazeID == nil || *show.TVmazeID != tvmazeID {
			return nil, staleLookup{}
		}
		if fetchErr != nil {
			return nil, s.tvmazeFailure(ctx, show.ID, tvmazeError{fetchErr})
		}
		show, art, err := s.applyMetadata(ctx, show, tv)
		if err != nil {
			return nil, err
		}
		return art, s.afterRefresh(ctx, show)
	})
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

// awaitingRematch: an unmatched show formed while TVmaze was down, never
// settled or touched by the owner since.
func (s *Service) awaitingRematch(ctx context.Context, show store.Show) (bool, error) {
	if show.TVmazeID != nil || show.RefreshedAt != nil {
		return false, nil
	}
	touched, err := s.ownerTouched(ctx, show)
	return !touched, err
}

// rematchQuery asks about the show's first numbered episode, with its parsed
// title and duration for the tie-breaks of ruling B11.
func (s *Service) rematchQuery(ctx context.Context, show store.Show) (showQuery, error) {
	q := showQuery{name: show.Title, key: library.ShowKey(show.Title), year: show.Year}
	items, err := s.store.ListItemsByShow(ctx, show.ID)
	if err != nil {
		return showQuery{}, err
	}
	for _, item := range items {
		if item.Episode == nil {
			continue
		}
		q.season, q.episode, _ = numbers(item)
		relPath, durationS, err := s.fileOf(ctx, item.ID)
		if err != nil {
			return showQuery{}, err
		}
		q.episodeTitle, q.durationS = library.ParsePath(relPath).EpisodeTitle, durationS
		break
	}
	return q, nil
}

// rematch retries matching an unmatched show formed while TVmaze was down.
// A weak result is never attached; a confident one whose show is already
// local merges this show into it, as classify would have attached there.
// Callers hold the lock.
func (s *Service) rematch(ctx context.Context, showID int64, lk *lookup) (*artJob, error) {
	show, err := s.store.GetShow(ctx, showID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if awaiting, err := s.awaitingRematch(ctx, show); err != nil || !awaiting {
		if err != nil {
			return nil, err
		}
		return nil, staleLookup{}
	}
	q, err := s.rematchQuery(ctx, show)
	if err != nil {
		return nil, err
	}
	if !lk.answers(q) {
		return nil, staleLookup{q}
	}
	if lk.err != nil {
		return nil, s.tvmazeFailure(ctx, show.ID, lk.err)
	}
	if !lk.m.confident {
		_, err := s.store.UpdateShow(ctx, show.ID, store.UpdateShowParams{RefreshedAt: store.SetTo(s.stamp())})
		return nil, err
	}
	s.log.Info("classify: show matched on TVmaze after an outage", "show_id", show.ID, "tvmaze_id", lk.m.result.ID)
	_, art, err := s.adopt(ctx, show, lk)
	if err != nil {
		return nil, s.tvmazeFailure(ctx, show.ID, err)
	}
	return art, nil
}

// adopt gives an unmatched show lk's confident TVmaze match: aliases are
// kept, metadata and episode titles refreshed. When another local show
// already has that match, show merges into it instead. It returns the show
// the items now belong to; a TVmaze error leaves the match set for a
// refresh. Callers hold the lock.
func (s *Service) adopt(ctx context.Context, show store.Show, lk *lookup) (store.Show, *artJob, error) {
	id := int64(lk.m.result.ID)
	local, err := s.store.GetShowByTVmazeID(ctx, id)
	if err == nil {
		if _, lockedItem, err := s.store.ShowOwnerMarks(ctx, show.ID); err != nil || lockedItem {
			if err != nil {
				return store.Show{}, nil, err
			}
			// Automation never moves a locked item between shows (ruling B17).
			s.log.Info("classify: matched show already local, kept apart for its locked items", "show_id", show.ID, "into", local.ID)
			show, err := s.store.UpdateShow(ctx, show.ID, store.UpdateShowParams{RefreshedAt: store.SetTo(s.stamp())})
			return show, nil, err
		}
		s.log.Info("classify: matched show already local, merging", "show_id", show.ID, "into", local.ID)
		if err := s.merge(ctx, show.ID, local.ID, false); err != nil {
			return store.Show{}, nil, err
		}
		local, err = s.store.GetShow(ctx, local.ID)
		return local, nil, err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Show{}, nil, err
	}
	if !lk.fetched() {
		return store.Show{}, nil, staleLookup{lk.q}
	}
	clear := false
	p := store.UpdateShowParams{TVmazeID: store.SetTo(id), NeedsCheck: &clear}
	if lk.m.viaAKA && !show.TitleLocked {
		p = akaTitle(lk.m.result.Name)
		p.TVmazeID, p.NeedsCheck = store.SetTo(id), &clear
	}
	if show, err = s.store.UpdateShow(ctx, show.ID, p); err != nil {
		return store.Show{}, nil, err
	}
	if lk.showErr != nil {
		return show, nil, lk.showErr
	}
	show, art, err := s.applyMetadata(ctx, show, *lk.show)
	if err != nil {
		return show, nil, err
	}
	return show, art, s.afterRefresh(ctx, show)
}

// applyMetadata copies TVmaze's show and episode list into the catalog and
// says which art to fetch. A title_locked title is never changed.
func (s *Service) applyMetadata(ctx context.Context, show store.Show, tv tvmaze.Show) (store.Show, *artJob, error) {
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
		return show, nil, err
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
	show, err := s.store.UpdateShow(ctx, show.ID, p)
	if err != nil {
		return show, nil, err
	}
	if show.PosterAt != nil && show.BackdropAt != nil {
		return show, nil, nil
	}
	return show, &artJob{showID: show.ID, tvmazeID: *show.TVmazeID, posterURL: tv.PosterURL}, nil
}

// akaTitle keeps the alternate name a show was matched by (ruling B13):
// locked, so refreshes never swap it back to TVmaze's primary name.
func akaTitle(name string) store.UpdateShowParams {
	locked := true
	return store.UpdateShowParams{Title: &name, TitleLocked: &locked}
}

// maxBackgroundAttempts bounds how many dead background links are tried.
const maxBackgroundAttempts = 4

// artJob is art a show still lacks, for the match it had when asked.
type artJob struct {
	showID, tvmazeID int64
	posterURL        string
}

// fetchArt downloads whichever of the poster and backdrop is not cached
// yet, trying backgrounds best first past dead links (ruling B7). It runs
// without the lock and installs the images only if the show still has the
// same match. Art is best effort: a failure leaves it for the next refresh.
func (s *Service) fetchArt(ctx context.Context, job *artJob) {
	if job == nil {
		return
	}
	show, err := s.store.GetShow(ctx, job.showID)
	if err != nil || show.TVmazeID == nil || *show.TVmazeID != job.tvmazeID {
		return
	}
	var poster, backdrop string
	if show.PosterAt == nil && job.posterURL != "" {
		if poster, err = s.prepareImage(ctx, job.posterURL, PosterMaxWidth); err != nil {
			s.log.Warn("classify: poster download failed", "show_id", show.ID, "error", err)
		}
	}
	if show.BackdropAt == nil {
		backgrounds, err := s.tvmaze.Backgrounds(ctx, int(job.tvmazeID))
		if err != nil {
			s.log.Warn("classify: backdrop lookup failed", "show_id", show.ID, "error", err)
		}
		for _, bg := range backgrounds[:min(len(backgrounds), maxBackgroundAttempts)] {
			if backdrop, err = s.prepareImage(ctx, bg.URL, BackdropMaxWidth); err == nil {
				break
			}
			s.log.Warn("classify: backdrop download failed", "show_id", show.ID, "error", err)
			if ctx.Err() != nil {
				break
			}
		}
	}
	defer removeIfSet(poster, backdrop)
	if poster == "" && backdrop == "" {
		return
	}
	if err := s.installArt(ctx, job, poster, backdrop); err != nil {
		s.log.Warn("classify: install show art", "show_id", show.ID, "error", err)
	}
}

func (s *Service) installArt(ctx context.Context, job *artJob, poster, backdrop string) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	show, err := s.store.GetShow(ctx, job.showID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || show.TVmazeID == nil || *show.TVmazeID != job.tvmazeID {
		return err
	}
	var p store.UpdateShowParams
	if poster != "" && show.PosterAt == nil {
		if err := os.Rename(poster, s.PosterPath(show.ID)); err != nil {
			return err
		}
		p.PosterAt = store.SetTo(s.stamp())
	}
	if backdrop != "" && show.BackdropAt == nil {
		if err := os.Rename(backdrop, s.BackdropPath(show.ID)); err != nil {
			return err
		}
		p.BackdropAt = store.SetTo(s.stamp())
	}
	if !p.PosterAt.Set && !p.BackdropAt.Set {
		return nil
	}
	if _, err := s.store.UpdateShow(ctx, show.ID, p); err != nil {
		return err
	}
	s.publish(events.ShowUpdated, show.ID)
	return nil
}

func removeIfSet(paths ...string) {
	for _, p := range paths {
		if p != "" {
			os.Remove(p)
		}
	}
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
