// Package classify files items as movies, videos or episodes of a show, and
// applies the owner's corrections so later arrivals follow them. It is the
// only writer of shows and aliases besides the store's own cascades.
package classify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/semantic"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tmdb"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

// UnreachableRetry is how long a TVmaze outage defers a show refresh.
const UnreachableRetry = time.Hour

const (
	movieMinS     = 60 * 60
	solWindowMinS = 30 * 60
)

// Scheduler queues show and movie refreshes; the job manager implements it.
type Scheduler interface {
	EnqueueShowRefresh(ctx context.Context, showID int64, runAt time.Time) error
	EnqueueMovieRefresh(ctx context.Context, itemID int64, runAt time.Time) error
}

// Semantic is the Sol classifier (*semantic.Classifier in production).
type Semantic interface {
	Classify(ctx context.Context, in semantic.Input) (semantic.Answer, error)
}

type Options struct {
	Store  *store.Store
	Bus    *events.Bus
	Log    *slog.Logger
	TVmaze *tvmaze.Client // shared process-wide: the throttle lives in it
	TMDB   *tmdb.Client   // nil or tokenless: movies are never matched
	Sol    Semantic       // nil disables semantic classification
	ArtDir string         // {data_dir}/art
	FFmpeg string         // binary that downscales oversized art; "" = ffmpeg on PATH
	Now    func() time.Time
}

type Service struct {
	store     *store.Store
	bus       *events.Bus
	log       *slog.Logger
	tvmaze    *tvmaze.Client
	tmdb      *tmdb.Client
	sol       Semantic
	artDir    string
	ffmpeg    string
	now       func() time.Time
	scheduler Scheduler
	// One classifier at a time, so two episodes of an unseen show can never
	// create two shows. A channel rather than a mutex so waiters honour ctx.
	// It guards catalog decisions only: TVmaze is asked before taking it
	// (lookUp) and art is fetched after, so a stalled request never holds
	// up other jobs or the owner's edits.
	sem chan struct{}
}

func New(opts Options) *Service {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FFmpeg == "" {
		opts.FFmpeg = "ffmpeg"
	}
	return &Service{
		store:  opts.Store,
		bus:    opts.Bus,
		log:    opts.Log,
		tvmaze: opts.TVmaze,
		tmdb:   opts.TMDB,
		sol:    opts.Sol,
		artDir: opts.ArtDir,
		ffmpeg: opts.FFmpeg,
		now:    opts.Now,
		sem:    make(chan struct{}, 1),
	}
}

// SetScheduler wires the job manager in; call it before jobs start.
func (s *Service) SetScheduler(sch Scheduler) { s.scheduler = sch }

func (s *Service) lock(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) unlock() { <-s.sem }

func (s *Service) locked(ctx context.Context, fn func() (*artJob, error)) (*artJob, error) {
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer s.unlock()
	return fn()
}

// maxStale bounds how often a decision is retried because the catalog
// changed between asking TVmaze and taking the lock.
const maxStale = 5

// MarkLegacyCutoff records, once, the moment this code first ran: items
// created before it were titled by the pre-shows parser and get the
// backfill's owner-edit check.
func (s *Service) MarkLegacyCutoff(ctx context.Context) error {
	_, err := s.store.SetSettingIfAbsent(ctx, store.SettingClassifyLegacyBefore, store.FormatTime(s.now()))
	return err
}

// guess is one reading of what an item is, from the parser or from Sol.
type guess struct {
	typ          string
	title        string
	year         *int
	showName     string
	showKey      string
	season       int
	episode      int
	episodeEnd   int
	episodeTitle string
}

func guessFrom(relPath string, p library.Parsed, durationS float64) guess {
	g := guess{title: p.Title, year: p.Year}
	if g.title == "" {
		base := path.Base(relPath)
		g.title = strings.TrimSuffix(base, path.Ext(base))
	}
	switch {
	case p.Kind == library.KindEpisode:
		g.typ = "episode"
		g.showName, g.showKey = p.ShowName, p.ShowKey
		g.season, g.episode, g.episodeEnd, g.episodeTitle = p.Season, p.Episode, p.EpisodeEnd, p.EpisodeTitle
	case durationS >= movieMinS:
		g.typ = "movie"
	default:
		g.typ = "video"
	}
	return g
}

func solGuess(a semantic.Answer, parsed guess) guess {
	if a.Kind != semantic.KindEpisode {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			title = parsed.title
		}
		return guess{typ: a.Kind, title: title, year: a.Year}
	}
	g := guess{
		typ:          "episode",
		showName:     strings.TrimSpace(a.ShowTitle),
		showKey:      library.ShowKey(a.ShowTitle),
		season:       1,
		episode:      *a.Episode,
		episodeTitle: strings.TrimSpace(a.EpisodeTitle),
		year:         a.Year,
	}
	switch {
	case a.Season != nil:
		g.season = *a.Season
	case parsed.typ == "episode":
		g.season = parsed.season
	}
	if a.EpisodeEnd != nil && *a.EpisodeEnd > g.episode {
		g.episodeEnd = *a.EpisodeEnd
	}
	g.title = fallbackEpisodeTitle(g.episodeTitle, g.episode, g.episodeEnd)
	return g
}

func fallbackEpisodeTitle(episodeTitle string, episode, episodeEnd int) string {
	switch {
	case episodeTitle != "":
		return episodeTitle
	case episodeEnd > episode:
		return fmt.Sprintf("Episodes %d–%d", episode, episodeEnd)
	default:
		return fmt.Sprintf("Episode %d", episode)
	}
}

// Guess is the offline guess item creation writes before classify runs: an
// episode marker makes an episode, else an hour or more of probed duration
// makes a movie, else a video.
func Guess(relPath string, durationS float64) store.NewItem {
	g := guessFrom(relPath, library.ParsePath(relPath), durationS)
	item := store.NewItem{Type: g.typ, Title: g.title, Year: g.year}
	if g.typ == "episode" {
		item.ShowKey = nonEmpty(g.showKey)
		item.Season, item.Episode = &g.season, &g.episode
		if g.episodeEnd > 0 {
			item.EpisodeEnd = &g.episodeEnd
		}
	}
	return item
}

// Classify runs the classify job for one item (design "Classification").
// TVmaze and Sol failures never fail it; only store errors do.
func (s *Service) Classify(ctx context.Context, itemID int64) error {
	item, err := s.store.GetItem(ctx, itemID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if item.ClassifiedAt != nil {
		return nil
	}
	relPath, durationS, err := s.fileOf(ctx, itemID)
	if err != nil {
		return err
	}
	if relPath == "" {
		s.log.Warn("classify: item has no file, left pending", "item_id", itemID)
		return nil
	}
	parsed := library.ParsePath(relPath)

	locked := item.Locked
	legacyLock := false
	if !locked {
		if legacyLock, err = s.ownerEdited(ctx, item, relPath); err != nil {
			return err
		}
		locked = legacyLock
	}

	f := filing{parsed: parsed, guess: guessFrom(relPath, parsed, durationS), needsCheck: !parsed.Sure,
		locked: locked, legacyLock: legacyLock, durationS: durationS}
	if !locked && s.wantsSol(ctx, parsed, f.guess, durationS) {
		answer, ok := s.askSol(ctx, relPath, durationS)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f.needsCheck = !ok
		if ok {
			f.guess = solGuess(answer, f.guess)
		}
	}

	q, ok := episodeQuery(f.guess)
	if locked {
		q, ok = lockedQuery(item, parsed)
	}
	if ok {
		if f.lookup, err = s.prepare(ctx, q); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		art, err := s.locked(ctx, func() (*artJob, error) { return s.file(ctx, itemID, f) })
		var stale staleLookup
		if !errors.As(err, &stale) || attempt == maxStale {
			s.fetchArt(ctx, art)
			if err != nil {
				return err
			}
			return s.matchMovie(ctx, itemID)
		}
		if f.lookup, err = s.lookUp(ctx, stale.q); err != nil {
			return err
		}
	}
}

// filing is what Classify learned about an item before taking the lock.
type filing struct {
	parsed     library.Parsed
	guess      guess
	needsCheck bool
	locked     bool
	legacyLock bool
	durationS  float64
	lookup     *lookup
}

// file makes the classify job's catalog decisions. Callers hold the lock.
func (s *Service) file(ctx context.Context, itemID int64, f filing) (*artJob, error) {
	// Re-read: the owner may have edited the item while Sol or TVmaze was
	// answering.
	item, err := s.store.GetItem(ctx, itemID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil || item.ClassifiedAt != nil {
		return nil, err
	}
	if f.locked || item.Locked {
		return s.classifyLocked(ctx, item, f.parsed, f.legacyLock && !item.Locked, f.durationS, f.lookup)
	}
	if f.guess.typ != "episode" {
		return nil, s.fileNonEpisode(ctx, item, f.guess, f.needsCheck)
	}
	return s.fileEpisode(ctx, item, f.guess, f.parsed, f.needsCheck, f.lookup)
}

func episodeQuery(g guess) (showQuery, bool) {
	if g.typ != "episode" || g.showKey == "" {
		return showQuery{}, false
	}
	return showQuery{name: g.showName, key: g.showKey, year: g.year, season: g.season, episode: g.episode}, true
}

// lockedQuery is the show query of a locked episode that has no show yet,
// which joins one through its path (build ruling B1).
func lockedQuery(item store.Item, parsed library.Parsed) (showQuery, bool) {
	key := derefOr(item.ShowKey, parsed.ShowKey)
	if item.Type != "episode" || item.ShowID != nil || key == "" {
		return showQuery{}, false
	}
	q := showQuery{name: parsed.ShowName, key: key, year: parsed.Year, season: parsed.Season, episode: parsed.Episode}
	if item.Episode != nil {
		q.season, q.episode, _ = numbers(item)
	}
	return q, true
}

// ownerEdited is the backfill lock heuristic: an item from before the
// upgrade whose title or type no longer matches what the old parser wrote
// was edited by the owner, so it is locked and keeps its values.
func (s *Service) ownerEdited(ctx context.Context, item store.Item, relPath string) (bool, error) {
	if legacy, err := s.legacy(ctx, item); err != nil || !legacy {
		return false, err
	}
	v1Type, v1Title, _ := library.LegacyTitle(relPath)
	if item.Title == v1Title && item.Type == v1Type {
		return false, nil
	}
	s.log.Info("classify: keeping owner edit", "item_id", item.ID, "title", item.Title, "type", item.Type)
	return true, nil
}

// legacy reports whether item predates the shows upgrade (MarkLegacyCutoff).
func (s *Service) legacy(ctx context.Context, item store.Item) (bool, error) {
	cutoff, err := s.store.GetSetting(ctx, store.SettingClassifyLegacyBefore)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil && item.CreatedAt < cutoff, err
}

// ApplyProbedDuration makes a pending, unlocked video a movie once a probe
// shows it runs an hour or more, for items created before their probe. A
// pre-upgrade item is left as the old parser wrote it, which the backfill's
// owner-edit check depends on.
func (s *Service) ApplyProbedDuration(ctx context.Context, item store.Item, relPath string, durationS float64) error {
	if item.ClassifiedAt != nil || item.Locked || item.Type != "video" || Guess(relPath, durationS).Type != "movie" {
		return nil
	}
	if legacy, err := s.legacy(ctx, item); err != nil || legacy {
		return err
	}
	movie := "movie"
	_, err := s.store.UpdateItem(ctx, item.ID, store.UpdateItemParams{Type: &movie})
	return err
}

func (s *Service) wantsSol(ctx context.Context, parsed library.Parsed, g guess, durationS float64) bool {
	if s.sol == nil {
		return false
	}
	setting, err := s.store.GetSetting(ctx, store.SettingSemanticClassification)
	if err != nil || setting != "1" {
		return false
	}
	ambiguousLength := g.typ != "episode" && durationS >= solWindowMinS && durationS < movieMinS
	return !parsed.Sure || ambiguousLength
}

// askSol reports ok only for a confident answer; failures are logged.
func (s *Service) askSol(ctx context.Context, relPath string, durationS float64) (semantic.Answer, bool) {
	shows, err := s.store.ListShows(ctx)
	if err != nil {
		s.log.Warn("classify: sol skipped, listing shows failed", "rel_path", relPath, "error", err)
		return semantic.Answer{}, false
	}
	titles := make([]string, len(shows))
	for i, sh := range shows {
		titles[i] = sh.Title
	}
	var folders []string
	if dir := path.Dir(relPath); dir != "." && dir != "/" {
		folders = strings.Split(strings.Trim(dir, "/"), "/")
	}
	answer, err := s.sol.Classify(ctx, semantic.Input{
		RelPath: relPath, Folders: folders, DurationSec: durationS, ShowTitles: titles,
	})
	if err != nil {
		s.log.Warn("classify: sol failed, keeping the parse", "rel_path", relPath, "error", err)
		return semantic.Answer{}, false
	}
	if !answer.Confident {
		s.log.Info("classify: sol unsure, keeping the parse", "rel_path", relPath)
		return answer, false
	}
	return answer, true
}

// classifyLocked files an item whose fields belong to the owner. A locked
// episode without a show joins one through its path so it never breaks the
// invariant (build ruling B1). A legacy lock still lets an hour-long video
// become a movie, since the old parser never wrote movie (ruling B7).
func (s *Service) classifyLocked(ctx context.Context, item store.Item, parsed library.Parsed, legacyLock bool, durationS float64, lk *lookup) (*artJob, error) {
	locked := true
	p := store.UpdateItemParams{Locked: &locked}
	if legacyLock && item.Type == "video" && durationS >= movieMinS {
		movie := "movie"
		p.Type = &movie
	}
	if item.Type != "episode" || item.ShowID != nil {
		p.ClassifiedAt = store.SetTo(s.stamp())
		return nil, s.updateItem(ctx, item.ID, p)
	}
	flag := true
	p.NeedsCheck = &flag
	q, ok := lockedQuery(item, parsed)
	if !ok {
		s.log.Warn("classify: locked episode has no show name, left pending", "item_id", item.ID)
		return nil, s.updateItem(ctx, item.ID, p)
	}
	show, created, art, err := s.showFor(ctx, q, lk)
	if err != nil {
		return nil, err
	}
	p.ShowID = store.SetTo(show.ID)
	p.ShowKey = store.SetTo(q.key)
	if item.Season == nil && item.Episode == nil && parsed.Kind == library.KindEpisode {
		p.Season, p.Episode = store.SetTo(parsed.Season), store.SetTo(parsed.Episode)
		if parsed.EpisodeEnd > 0 {
			p.EpisodeEnd = store.SetTo(parsed.EpisodeEnd)
		}
	}
	p.ClassifiedAt = store.SetTo(s.stamp())
	if err := s.updateItem(ctx, item.ID, p); err != nil {
		return nil, err
	}
	s.publishShow(show.ID, created)
	return art, nil
}

func (s *Service) fileNonEpisode(ctx context.Context, item store.Item, g guess, needsCheck bool) error {
	return s.updateItem(ctx, item.ID, store.UpdateItemParams{
		Type:         &g.typ,
		Title:        &g.title,
		Year:         store.Nullable[int]{Set: true, Value: g.year},
		ShowID:       store.SetNull[int64](),
		Season:       store.SetNull[int](),
		Episode:      store.SetNull[int](),
		EpisodeEnd:   store.SetNull[int](),
		ShowKey:      store.SetNull[string](),
		NeedsCheck:   &needsCheck,
		ClassifiedAt: store.SetTo(s.stamp()),
	})
}

func (s *Service) fileEpisode(ctx context.Context, item store.Item, g guess, parsed library.Parsed, needsCheck bool, lk *lookup) (*artJob, error) {
	itemKey := parsed.ShowKey
	if itemKey == "" {
		itemKey = g.showKey
	}
	p := store.UpdateItemParams{
		Type:       &g.typ,
		Title:      &g.title,
		Year:       store.Nullable[int]{Set: true, Value: g.year},
		Season:     store.SetTo(g.season),
		Episode:    store.SetTo(g.episode),
		EpisodeEnd: store.SetNull[int](),
		ShowKey:    nullableString(itemKey),
		NeedsCheck: &needsCheck,
	}
	if g.episodeEnd > 0 {
		p.EpisodeEnd = store.SetTo(g.episodeEnd)
	}
	q, ok := episodeQuery(g)
	if !ok {
		// Nothing to match on: stay a pending episode with a Check mark
		// until the owner moves it to a show.
		flag := true
		p.NeedsCheck = &flag
		s.log.Warn("classify: episode has no show name, left pending", "item_id", item.ID)
		return nil, s.updateItem(ctx, item.ID, p)
	}
	show, created, art, err := s.showFor(ctx, q, lk)
	if err != nil {
		return nil, err
	}
	eps, err := s.episodeTitles(ctx, show.ID)
	if err != nil {
		return nil, err
	}
	title := episodeTitle(eps, show.SeasonOffset, g.season, g.episode, g.episodeEnd, g.episodeTitle)
	p.Title = &title
	p.ShowID = store.SetTo(show.ID)
	p.ClassifiedAt = store.SetTo(s.stamp())
	if err := s.updateItem(ctx, item.ID, p); err != nil {
		return nil, err
	}
	s.publishShow(show.ID, created)
	return art, nil
}

// showFor finds or forms the show an episode with this key joins (design
// "Classification" step 3), from what lk learned from TVmaze. Callers hold
// the lock; staleLookup asks them to look q up and decide again.
func (s *Service) showFor(ctx context.Context, q showQuery, lk *lookup) (store.Show, bool, *artJob, error) {
	alias, err := s.store.GetShowAlias(ctx, q.key)
	if err == nil {
		show, err := s.store.GetShow(ctx, alias.ShowID)
		if err != nil || !upgradable(alias, show) {
			return show, false, nil, err
		}
		if !lk.answers(q) {
			return store.Show{}, false, nil, staleLookup{q}
		}
		show, art, err := s.upgrade(ctx, show, lk)
		return show, false, art, err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Show{}, false, nil, err
	}
	if !lk.answers(q) {
		return store.Show{}, false, nil, staleLookup{q}
	}

	var show store.Show
	var art *artJob
	created := true
	switch {
	case lk.err == nil && lk.m.confident:
		local, err := s.store.GetShowByTVmazeID(ctx, int64(lk.m.result.ID))
		if err == nil {
			show, created = local, false
			break
		}
		if !errors.Is(err, store.ErrNotFound) {
			return store.Show{}, false, nil, err
		}
		if !lk.fetched() {
			return store.Show{}, false, nil, staleLookup{q}
		}
		if show, art, err = s.createMatched(ctx, lk); err != nil {
			return store.Show{}, false, nil, err
		}
	default:
		if show, err = s.createUnmatched(ctx, q.name, q.key, q.year, lk.err); err != nil {
			return store.Show{}, false, nil, err
		}
	}
	if _, err := s.store.PutShowAlias(ctx, q.key, show.ID, store.AliasAuto); err != nil {
		return store.Show{}, false, nil, err
	}
	return show, created, art, nil
}

// upgradable: a later arrival may still match a flagged unmatched show its
// auto alias points at (ruling B7). An owner alias, or a show the owner
// settled, never is.
func upgradable(alias store.ShowAlias, show store.Show) bool {
	return alias.Source == store.AliasAuto && show.TVmazeID == nil && show.NeedsCheck
}

// upgrade lets a later arrival match a flagged unmatched show, so arrival
// order no longer decides (ruling B7). It returns the show the arrival
// joins: show itself, or the local show already holding the match, which
// show merges into.
func (s *Service) upgrade(ctx context.Context, show store.Show, lk *lookup) (store.Show, *artJob, error) {
	if lk.err != nil || !lk.m.confident {
		return show, nil, nil
	}
	s.log.Info("classify: unmatched show matched by a later arrival", "show_id", show.ID, "tvmaze_id", lk.m.result.ID)
	adopted, art, err := s.adopt(ctx, show, lk)
	if err == nil || !isTVmaze(err) {
		return adopted, art, err
	}
	s.log.Warn("classify: TVmaze show fetch failed, refreshing later", "show_id", adopted.ID, "error", err)
	s.scheduleRefresh(ctx, adopted.ID, s.now().Add(UnreachableRetry))
	return adopted, nil, nil
}

func (s *Service) createMatched(ctx context.Context, lk *lookup) (store.Show, *artJob, error) {
	r := lk.m.result
	id := int64(r.ID)
	show, err := s.store.CreateShow(ctx, store.NewShow{Title: r.Name, Year: yearPtr(r.Year), Network: nonEmpty(r.Network), TVmazeID: &id})
	if err != nil {
		return store.Show{}, nil, err
	}
	s.log.Info("classify: new show matched on TVmaze", "show_id", show.ID, "title", r.Name, "tvmaze_id", r.ID)
	if lk.showErr != nil {
		s.log.Warn("classify: TVmaze show fetch failed, refreshing later", "show_id", show.ID, "error", lk.showErr)
		s.scheduleRefresh(ctx, show.ID, s.now().Add(UnreachableRetry))
		return show, nil, nil
	}
	return s.applyMetadata(ctx, show, *lk.show)
}

func (s *Service) createUnmatched(ctx context.Context, name, key string, year *int, searchErr error) (store.Show, error) {
	title := strings.TrimSpace(name)
	if title == "" {
		title = key
	}
	show, err := s.store.CreateShow(ctx, store.NewShow{Title: title, Year: year, NeedsCheck: true})
	if err != nil {
		return store.Show{}, err
	}
	if searchErr != nil {
		s.log.Warn("classify: TVmaze unreachable, show formed locally", "show_id", show.ID, "title", title, "error", searchErr)
		s.scheduleRefresh(ctx, show.ID, s.now().Add(UnreachableRetry))
		return show, nil
	}
	// TVmaze answered and had no confident match: settled, so a refresh
	// never re-matches it behind the owner's back.
	s.log.Info("classify: no confident TVmaze match, show formed unmatched", "show_id", show.ID, "title", title)
	return s.store.UpdateShow(ctx, show.ID, store.UpdateShowParams{RefreshedAt: store.SetTo(s.stamp())})
}

func (s *Service) scheduleRefresh(ctx context.Context, showID int64, at time.Time) {
	if s.scheduler == nil {
		s.log.Warn("classify: no scheduler for show refresh", "show_id", showID)
		return
	}
	if err := s.scheduler.EnqueueShowRefresh(ctx, showID, at); err != nil {
		s.log.Error("classify: enqueue show refresh", "show_id", showID, "error", err)
	}
}

// fileOf returns the item's first file's path and probed duration.
func (s *Service) fileOf(ctx context.Context, itemID int64) (string, float64, error) {
	files, err := s.store.ListFilesForItem(ctx, itemID)
	if err != nil || len(files) == 0 {
		return "", 0, err
	}
	var duration float64
	if files[0].DurationS != nil {
		duration = *files[0].DurationS
	}
	return files[0].RelPath, duration, nil
}

func (s *Service) updateItem(ctx context.Context, id int64, p store.UpdateItemParams) error {
	if _, err := s.store.UpdateItem(ctx, id, p); err != nil {
		return err
	}
	s.publishItem(ctx, id)
	return nil
}

func (s *Service) stamp() string { return store.FormatTime(s.now()) }

func (s *Service) publishItem(ctx context.Context, id int64) {
	if s.bus == nil {
		return
	}
	summary, err := s.store.GetItemSummary(ctx, id)
	if err != nil {
		s.log.Error("hydrate item event", "item_id", id, "error", err)
		return
	}
	s.bus.Publish(events.Event{Type: events.ItemUpdated, Payload: summary})
}

func (s *Service) publishShow(id int64, added bool) {
	typ := events.ShowUpdated
	if added {
		typ = events.ShowAdded
	}
	s.publish(typ, id)
}

func (s *Service) publish(typ string, showID int64) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: typ, Payload: map[string]any{"id": showID}})
	}
}

func yearPtr(y int) *int {
	if y == 0 {
		return nil
	}
	return &y
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableString(s string) store.Nullable[string] {
	if s == "" {
		return store.SetNull[string]()
	}
	return store.SetTo(s)
}

func derefOr(p *string, fallback string) string {
	if p != nil && *p != "" {
		return *p
	}
	return fallback
}
