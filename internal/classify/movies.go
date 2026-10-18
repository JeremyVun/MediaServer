package classify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tmdb"
)

var (
	// ErrTMDBUnreachable marks a movie refresh TMDB could not answer; the
	// job layer retries it after UnreachableRetry.
	ErrTMDBUnreachable = errors.New("tmdb unreachable")
	// ErrTMDBNotConfigured rejects choosing a TMDB match with no token set.
	ErrTMDBNotConfigured = errors.New("tmdb is not configured")
)

// TMDBConfigured reports whether movie matching may make TMDB requests.
func (s *Service) TMDBConfigured() bool { return s.tmdb.Configured() }

// ArtPath is where an item's cached TMDB backdrop lives.
func (s *Service) ArtPath(itemID int64) string {
	return filepath.Join(s.artDir, "items", fmt.Sprintf("%d-backdrop.jpg", itemID))
}

func (s *Service) removeItemArt(itemID int64) {
	if err := os.Remove(s.ArtPath(itemID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("classify: remove item art", "item_id", itemID, "error", err)
	}
}

// matchMovie runs TMDB matching at the end of a classify job. An outage
// never fails classify: a movie_refresh retries in an hour.
func (s *Service) matchMovie(ctx context.Context, itemID int64) error {
	if !s.TMDBConfigured() {
		return nil
	}
	err := s.RefreshMovie(ctx, itemID)
	if !errors.Is(err, ErrTMDBUnreachable) {
		return err
	}
	s.log.Warn("classify: TMDB unreachable, matching the movie later", "item_id", itemID, "error", err)
	s.scheduleMovieRefresh(ctx, itemID, s.now().Add(UnreachableRetry))
	return nil
}

func (s *Service) scheduleMovieRefresh(ctx context.Context, itemID int64, at time.Time) {
	if s.scheduler == nil {
		s.log.Warn("classify: no scheduler for movie refresh", "item_id", itemID)
		return
	}
	if err := s.scheduler.EnqueueMovieRefresh(ctx, itemID, at); err != nil {
		s.log.Error("classify: enqueue movie refresh", "item_id", itemID, "error", err)
	}
}

// movieAnswer is what TMDB said about one movie, gathered without the lock.
type movieAnswer struct {
	asked    store.Item // the item as it was when TMDB was asked
	movie    *tmdb.Movie
	backdrop string // downloaded temp file, "" when none
}

// RefreshMovie runs the movie_refresh job for one item: a movie TMDB
// matching never settled is matched (design "Movie metadata: TMDB"), and a
// matched one gets its missing summary and backdrop. TMDB is asked before
// the lock and the item re-read under it. An error wrapping
// ErrTMDBUnreachable asks for a retry later.
func (s *Service) RefreshMovie(ctx context.Context, itemID int64) error {
	if !s.TMDBConfigured() {
		return nil
	}
	for attempt := 0; ; attempt++ {
		item, err := s.store.GetItem(ctx, itemID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !wantsTMDB(item) {
			return nil
		}
		answer, err := s.askTMDB(ctx, item)
		if err != nil {
			removeIfSet(answer.backdrop)
			return err
		}
		stale, err := s.applyMovie(ctx, answer)
		removeIfSet(answer.backdrop)
		if !stale || err != nil || attempt == maxStale {
			return err
		}
	}
}

func wantsTMDB(item store.Item) bool {
	return item.Type == "movie" && item.DeletedAt == nil && (item.TMDBID != nil || item.TMDBCheckedAt == nil)
}

func (s *Service) askTMDB(ctx context.Context, item store.Item) (movieAnswer, error) {
	answer := movieAnswer{asked: item}
	var movie tmdb.Movie
	switch {
	case item.TMDBID != nil:
		m, err := s.tmdb.Movie(ctx, int(*item.TMDBID))
		if errors.Is(err, tmdb.ErrNotFound) {
			s.log.Warn("classify: TMDB no longer has this movie", "item_id", item.ID, "tmdb_id", *item.TMDBID)
			return answer, nil
		}
		if err != nil {
			return answer, s.tmdbFailure(ctx, err)
		}
		movie = m
	case item.Year == nil:
		// Never confident without a year, so asking would be wasted.
		return answer, nil
	default:
		results, err := s.tmdb.SearchMovie(ctx, item.Title, *item.Year)
		if err != nil {
			return answer, s.tmdbFailure(ctx, err)
		}
		m, ok := confidentMovie(results, item.Title, *item.Year)
		if !ok {
			if len(results) > 0 {
				s.log.Info("classify: weak TMDB result not attached", "item_id", item.ID, "title", item.Title,
					"candidate", results[0].Title, "candidate_year", results[0].Year, "tmdb_id", results[0].ID)
			}
			return answer, nil
		}
		movie = m
	}
	answer.movie = &movie
	if item.ArtAt == nil && movie.BackdropPath != "" {
		path, err := s.downloadMovieArt(ctx, s.tmdb.ImageURL(tmdb.BackdropSize, movie.BackdropPath))
		answer.backdrop = path
		if ctx.Err() != nil {
			return answer, ctx.Err()
		}
		if err != nil {
			s.log.Warn("classify: movie backdrop download failed", "item_id", item.ID, "error", err)
		}
	}
	return answer, nil
}

func (s *Service) tmdbFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %w", ErrTMDBUnreachable, err)
}

// confidentMovie: exactly one result whose title (or original title)
// normalizes to the item's and whose release year is the item's.
func confidentMovie(results []tmdb.Movie, title string, year int) (tmdb.Movie, bool) {
	key := library.ShowKey(title)
	var found []tmdb.Movie
	for _, r := range results {
		if r.Year != year {
			continue
		}
		if library.ShowKey(r.Title) == key || (r.OriginalTitle != "" && library.ShowKey(r.OriginalTitle) == key) {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		return tmdb.Movie{}, false
	}
	return found[0], true
}

func (s *Service) downloadMovieArt(ctx context.Context, url string) (string, error) {
	dir := filepath.Join(s.artDir, "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".art-*.jpg")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := s.tmdb.FetchImage(ctx, url, path); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// applyMovie writes TMDB's answer under the lock. stale means the item
// changed while TMDB was asked, so the answer no longer fits.
func (s *Service) applyMovie(ctx context.Context, a movieAnswer) (stale bool, err error) {
	if err := s.lock(ctx); err != nil {
		return false, err
	}
	defer s.unlock()
	item, err := s.store.GetItem(ctx, a.asked.ID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !wantsTMDB(item) {
		return false, nil
	}
	if !sameTMDBQuestion(a.asked, item) {
		return true, nil
	}

	p := store.UpdateItemParams{TMDBCheckedAt: store.SetTo(s.stamp())}
	switch {
	case a.movie == nil && item.TMDBID == nil:
		flag := true
		p.NeedsCheck = &flag
		s.log.Info("classify: movie not matched on TMDB", "item_id", item.ID, "title", item.Title)
	case a.movie != nil:
		if item.TMDBID == nil {
			p.TMDBID = store.SetTo(int64(a.movie.ID))
			s.log.Info("classify: movie matched on TMDB", "item_id", item.ID, "title", a.movie.Title, "tmdb_id", a.movie.ID)
		}
		if overview := a.movie.Overview; overview != "" && fillsSummary(item, a.asked.TMDBID != nil) {
			p.Summary = store.SetTo(overview)
		}
		if a.backdrop != "" && item.ArtAt == nil {
			if err := os.Rename(a.backdrop, s.ArtPath(item.ID)); err != nil {
				return false, err
			}
			p.ArtAt = store.SetTo(s.stamp())
		}
	}
	return false, s.updateItem(ctx, item.ID, p)
}

// sameTMDBQuestion: the item still asks TMDB what it asked before.
func sameTMDBQuestion(asked, now store.Item) bool {
	sameID := (asked.TMDBID == nil) == (now.TMDBID == nil) && (asked.TMDBID == nil || *asked.TMDBID == *now.TMDBID)
	sameYear := (asked.Year == nil) == (now.Year == nil) && (asked.Year == nil || *asked.Year == *now.Year)
	return sameID && sameYear && asked.Title == now.Title && (asked.ArtAt == nil) == (now.ArtAt == nil)
}

// fillsSummary: TMDB's overview never replaces the owner's summary, and an
// automatic match leaves a locked item's alone too. A match the owner chose
// (ChooseMovieMatch, which clears the old automatic summary) fills it.
func fillsSummary(item store.Item, chosen bool) bool {
	return !item.SummaryLocked && (chosen || !item.Locked)
}

// ChooseMovieMatch is the Change dialog's TMDB choice for a movie: a TMDB
// movie, or nil for "Not on TMDB". Either clears the Check mark and settles
// matching. A new match drops the old backdrop and automatic summary and
// queues a movie_refresh, which fetches the new ones.
func (s *Service) ChooseMovieMatch(ctx context.Context, itemID int64, tmdbID *int64) (store.Item, error) {
	if tmdbID != nil && *tmdbID <= 0 {
		return store.Item{}, fmt.Errorf("bad tmdb id: %w", store.ErrInvalidInput)
	}
	if tmdbID != nil && !s.TMDBConfigured() {
		return store.Item{}, ErrTMDBNotConfigured
	}
	if err := s.lock(ctx); err != nil {
		return store.Item{}, err
	}
	defer s.unlock()
	item, err := s.store.GetItem(ctx, itemID)
	if err != nil {
		return store.Item{}, err
	}
	if item.Type != "movie" {
		return store.Item{}, fmt.Errorf("only a movie has a TMDB match: %w", store.ErrInvalidInput)
	}
	same := tmdbID != nil && item.TMDBID != nil && *item.TMDBID == *tmdbID
	clear := false
	p := store.UpdateItemParams{NeedsCheck: &clear, TMDBCheckedAt: store.SetTo(s.stamp())}
	if !same {
		p.TMDBID = store.Nullable[int64]{Set: true, Value: tmdbID}
		p.ArtAt = store.SetNull[string]()
		if !item.SummaryLocked && item.Summary != nil {
			p.Summary = store.SetNull[string]()
		}
	}
	updated, err := s.store.UpdateItem(ctx, item.ID, p)
	if err != nil {
		return store.Item{}, err
	}
	if !same {
		s.removeItemArt(item.ID)
	}
	s.publishItem(ctx, item.ID)
	if tmdbID != nil {
		s.scheduleMovieRefresh(ctx, item.ID, s.now())
	}
	return updated, nil
}
