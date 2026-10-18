package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/library"
)

const (
	EntryItem = "item"
	EntryShow = "show"
)

// Entry is one library grid card: an item outside any show, or a show.
type Entry struct {
	Kind string // EntryItem|EntryShow
	Item *ItemSummary
	Show *ShowSummary
}

type ShowSummary struct {
	ID            int64
	Title         string
	Year          *int
	Network       *string
	PosterAt      *string
	BackdropAt    *string
	CreatedAt     string
	LatestAddedAt string // newest live episode's created_at
	SeasonCount   int    // distinct seasons in the library, specials excluded
	EpisodeCount  int    // live episode items
	Available     bool   // any live episode is playable now
	NeedsCheck    bool
	HasProgress   bool // any episode started or completed
	// Collections holding the show itself or any of its live episodes, so
	// per-show collections made from episodes keep finding the show.
	CollectionIDs []int64
	UpNext        *UpNextEpisode // nil = caught up
}

type UpNextEpisode struct {
	ItemID     int64
	Season     int
	Episode    int
	EpisodeEnd *int
	Title      string
	Available  bool // false = offline; still the true next episode (ruling M10)
	Progress   *Progress
}

// ListEntriesOpts mirrors GET /api/library query parameters.
type ListEntriesOpts struct {
	Sort         string // added|title|year|watched (default added; collection order inside a collection)
	Order        string // asc|desc (default desc for added/watched, asc otherwise)
	Type         string // ""|show|movie
	CollectionID int64
	Uncollected  bool // ignored when CollectionID > 0
	InProgress   bool
	Query        string // FTS over show and item titles; an episode hit surfaces its show
	Offset       int
	Limit        int // <=0 means default 60, capped at 500
}

type entryKey struct {
	kind      string
	id        int64
	added     string
	title     string
	year      *int
	watched   *string
	collOrder *int
}

// ListEntries returns one page of the library grid and the total count.
// Items are every live item with no show, of any type (build ruling B1);
// shows are every show with a live episode.
func (s *Store) ListEntries(ctx context.Context, opts ListEntriesOpts) ([]Entry, int, error) {
	less, err := entryOrder(opts)
	if err != nil {
		return nil, 0, err
	}
	match := ""
	if strings.TrimSpace(opts.Query) != "" {
		if match = FTS5PrefixQuery(opts.Query); match == "" {
			return []Entry{}, 0, nil
		}
	}

	var keys []entryKey
	if opts.Type != EntryShow {
		itemKeys, err := s.itemEntryKeys(ctx, opts, match)
		if err != nil {
			return nil, 0, err
		}
		keys = append(keys, itemKeys...)
	}
	var states map[int64][]episodeRow
	if opts.Type != "movie" {
		showKeys, err := s.showEntryKeys(ctx, opts, match)
		if err != nil {
			return nil, 0, err
		}
		if states, err = s.episodeRows(ctx, 0); err != nil {
			return nil, 0, err
		}
		for _, k := range showKeys {
			if opts.InProgress && !continueWatching(states[k.id]) {
				continue
			}
			keys = append(keys, k)
		}
	}

	sort.SliceStable(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
	total := len(keys)
	limit := opts.Limit
	if limit <= 0 {
		limit = 60
	}
	limit = min(limit, 500)
	start := min(max(opts.Offset, 0), total)
	page := keys[start:min(start+limit, total)]

	var itemIDs, showIDs []int64
	for _, k := range page {
		if k.kind == EntryItem {
			itemIDs = append(itemIDs, k.id)
		} else {
			showIDs = append(showIDs, k.id)
		}
	}
	items, err := s.itemSummariesByID(ctx, itemIDs)
	if err != nil {
		return nil, 0, err
	}
	shows, err := s.showSummaries(ctx, showIDs, states)
	if err != nil {
		return nil, 0, err
	}
	itemByID := make(map[int64]*ItemSummary, len(items))
	for i := range items {
		itemByID[items[i].ID] = &items[i]
	}
	entries := make([]Entry, 0, len(page))
	for _, k := range page {
		if k.kind == EntryItem {
			if it := itemByID[k.id]; it != nil {
				entries = append(entries, Entry{Kind: EntryItem, Item: it})
			}
		} else if sh := shows[k.id]; sh != nil {
			entries = append(entries, Entry{Kind: EntryShow, Show: sh})
		}
	}
	return entries, total, nil
}

// GetShowSummary loads one show in the library-entry shape, live episodes
// or not.
func (s *Store) GetShowSummary(ctx context.Context, id int64) (ShowSummary, error) {
	states, err := s.episodeRows(ctx, id)
	if err != nil {
		return ShowSummary{}, err
	}
	shows, err := s.showSummaries(ctx, []int64{id}, states)
	if err != nil {
		return ShowSummary{}, err
	}
	sh := shows[id]
	if sh == nil {
		return ShowSummary{}, ErrNotFound
	}
	return *sh, nil
}

// ListShowItems returns the show's live episodes in episode order, specials
// last.
func (s *Store) ListShowItems(ctx context.Context, showID int64) ([]ItemSummary, error) {
	rows, err := s.db.QueryContext(ctx, summarySelect()+`
		WHERE mi.show_id = ? AND mi.deleted_at IS NULL
		GROUP BY mi.id
		ORDER BY COALESCE(mi.season, 1) = 0, COALESCE(mi.season, 1), COALESCE(mi.episode, 0), mi.id`, showID)
	if err != nil {
		return nil, err
	}
	items, err := scanSummaries(rows)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []ItemSummary{}
	}
	return items, s.loadSummaryRelations(ctx, items)
}

// ShowEpisodeStates returns the show's live episodes as UpNext and
// NextEpisode take them.
func (s *Store) ShowEpisodeStates(ctx context.Context, showID int64) ([]library.EpisodeState, error) {
	rows, err := s.episodeRows(ctx, showID)
	if err != nil {
		return nil, err
	}
	return episodeStates(rows[showID]), nil
}

func entryOrder(opts ListEntriesOpts) (func(a, b entryKey) bool, error) {
	switch opts.Type {
	case "", EntryShow, "movie":
	default:
		return nil, fmt.Errorf("bad type %q: %w", opts.Type, ErrInvalidInput)
	}
	var cmp func(a, b entryKey) int
	desc := true
	switch opts.Sort {
	case "", "added":
		cmp = func(a, b entryKey) int { return strings.Compare(a.added, b.added) }
		if opts.Sort == "" && opts.CollectionID > 0 {
			cmp = func(a, b entryKey) int { return compareNullable(a.collOrder, b.collOrder) }
			desc = false
		}
	case "title":
		cmp = func(a, b entryKey) int { return strings.Compare(a.title, b.title) }
		desc = false
	case "year":
		cmp = func(a, b entryKey) int { return compareNullable(a.year, b.year) }
		desc = false
	case "watched":
		cmp = func(a, b entryKey) int { return compareNullable(a.watched, b.watched) }
	default:
		return nil, fmt.Errorf("bad sort %q: %w", opts.Sort, ErrInvalidInput)
	}
	switch strings.ToLower(opts.Order) {
	case "":
	case "asc":
		desc = false
	case "desc":
		desc = true
	default:
		return nil, fmt.Errorf("bad order %q: %w", opts.Order, ErrInvalidInput)
	}
	return func(a, b entryKey) bool {
		c := cmp(a, b)
		if c == 0 {
			c = compareInt64(a.id, b.id)
		}
		if c == 0 {
			c = strings.Compare(a.kind, b.kind)
		}
		if desc {
			return c > 0
		}
		return c < 0
	}, nil
}

// compareNullable sorts nil first, as SQLite orders NULL in ascending sorts.
func compareNullable[T int | string](a, b *T) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	}
	return 0
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *Store) itemEntryKeys(ctx context.Context, opts ListEntriesOpts, match string) ([]entryKey, error) {
	where := "mi.deleted_at IS NULL AND mi.show_id IS NULL"
	collOrder := "NULL"
	args := []any{}
	if opts.CollectionID > 0 {
		collOrder = "(SELECT ci.sort_order FROM collection_items ci WHERE ci.collection_id = ? AND ci.item_id = mi.id)"
		args = append(args, opts.CollectionID)
	}
	if opts.Type == "movie" {
		where += " AND mi.type = 'movie'"
	}
	if opts.CollectionID > 0 {
		where += " AND EXISTS (SELECT 1 FROM collection_items ci WHERE ci.collection_id = ? AND ci.item_id = mi.id)"
		args = append(args, opts.CollectionID)
	} else if opts.Uncollected {
		where += " AND NOT EXISTS (SELECT 1 FROM collection_items ci WHERE ci.item_id = mi.id)"
	}
	if opts.InProgress {
		where += " AND wp.position_s > 0 AND wp.completed = 0"
	}
	if match != "" {
		where += " AND mi.id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)"
		args = append(args, match)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT mi.id, mi.created_at, mi.title, mi.year, wp.updated_at, `+collOrder+`
		FROM media_items mi
		LEFT JOIN watch_progress wp ON wp.item_id = mi.id
		WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	return scanEntryKeys(rows, EntryItem)
}

// showMemberSQL matches collection rows holding the show or one of its live
// episodes, correlated on sh.id.
const showMemberSQL = `(ci.show_id = sh.id OR ci.item_id IN (
	SELECT e.id FROM media_items e WHERE e.show_id = sh.id AND e.deleted_at IS NULL))`

func (s *Store) showEntryKeys(ctx context.Context, opts ListEntriesOpts, match string) ([]entryKey, error) {
	where := "1 = 1"
	collOrder := "NULL"
	args := []any{}
	if opts.CollectionID > 0 {
		collOrder = "(SELECT MIN(ci.sort_order) FROM collection_items ci WHERE ci.collection_id = ? AND " + showMemberSQL + ")"
		args = append(args, opts.CollectionID)
		where += " AND EXISTS (SELECT 1 FROM collection_items ci WHERE ci.collection_id = ? AND " + showMemberSQL + ")"
		args = append(args, opts.CollectionID)
	} else if opts.Uncollected {
		where += " AND NOT EXISTS (SELECT 1 FROM collection_items ci WHERE " + showMemberSQL + ")"
	}
	if match != "" {
		where += ` AND (sh.id IN (SELECT rowid FROM shows_fts WHERE shows_fts MATCH ?)
			OR EXISTS (SELECT 1 FROM media_items e
			           WHERE e.show_id = sh.id AND e.deleted_at IS NULL
			             AND e.id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)))`
		args = append(args, match, match)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sh.id, MAX(mi.created_at), sh.title, sh.year, MAX(wp.updated_at), `+collOrder+`
		FROM shows sh
		JOIN media_items mi ON mi.show_id = sh.id AND mi.deleted_at IS NULL
		LEFT JOIN watch_progress wp ON wp.item_id = mi.id
		WHERE `+where+`
		GROUP BY sh.id`, args...)
	if err != nil {
		return nil, err
	}
	return scanEntryKeys(rows, EntryShow)
}

func scanEntryKeys(rows *sql.Rows, kind string) ([]entryKey, error) {
	defer rows.Close()
	var keys []entryKey
	for rows.Next() {
		k := entryKey{kind: kind}
		var year, collOrder sql.NullInt64
		var watched sql.NullString
		if err := rows.Scan(&k.id, &k.added, &k.title, &year, &watched, &collOrder); err != nil {
			return nil, err
		}
		k.title = strings.ToLower(k.title)
		k.year = intPtr(year)
		k.watched = stringPtr(watched)
		k.collOrder = intPtr(collOrder)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

type episodeRow struct {
	state     library.EpisodeState
	title     string
	createdAt string
	progress  *Progress
}

// episodeRows loads every live episode of one show (showID > 0) or of all
// shows, grouped by show.
func (s *Store) episodeRows(ctx context.Context, showID int64) (map[int64][]episodeRow, error) {
	filter := "mi.show_id IS NOT NULL"
	args := []any{}
	if showID > 0 {
		filter = "mi.show_id = ?"
		args = append(args, showID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT mi.show_id, mi.id, mi.title, mi.created_at,
		       COALESCE(mi.season, 1), COALESCE(mi.episode, 0), mi.episode_end,
		       CASE WHEN SUM(CASE WHEN mf.status = 'online' AND lr.online = 1 THEN 1 ELSE 0 END) > 0 THEN 1 ELSE 0 END,
		       wp.position_s, wp.duration_s, wp.completed, wp.updated_at
		FROM media_items mi
		LEFT JOIN media_files mf ON mf.item_id = mi.id
		LEFT JOIN library_roots lr ON lr.id = mf.root_id
		LEFT JOIN watch_progress wp ON wp.item_id = mi.id
		WHERE mi.deleted_at IS NULL AND `+filter+`
		GROUP BY mi.id
		ORDER BY mi.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byShow := map[int64][]episodeRow{}
	for rows.Next() {
		var show int64
		var r episodeRow
		var episodeEnd, completed sql.NullInt64
		var available int
		var position, duration sql.NullFloat64
		var updatedAt sql.NullString
		if err := rows.Scan(&show, &r.state.ItemID, &r.title, &r.createdAt,
			&r.state.Season, &r.state.Episode, &episodeEnd, &available,
			&position, &duration, &completed, &updatedAt); err != nil {
			return nil, err
		}
		r.state.EpisodeEnd = int(episodeEnd.Int64)
		r.state.Available = available != 0
		if position.Valid {
			r.progress = &Progress{
				PositionS: position.Float64,
				DurationS: duration.Float64,
				Completed: completed.Int64 != 0,
				UpdatedAt: updatedAt.String,
			}
			at, _ := ParseTime(updatedAt.String)
			r.state.Progress = &library.EpisodeProgress{
				PositionS: position.Float64,
				Completed: r.progress.Completed,
				UpdatedAt: at,
			}
		}
		byShow[show] = append(byShow[show], r)
	}
	return byShow, rows.Err()
}

func episodeStates(rows []episodeRow) []library.EpisodeState {
	states := make([]library.EpisodeState, len(rows))
	for i, r := range rows {
		states[i] = r.state
	}
	return states
}

func hasProgress(rows []episodeRow) bool {
	for _, r := range rows {
		if r.progress != nil && (r.progress.PositionS > 0 || r.progress.Completed) {
			return true
		}
	}
	return false
}

// continueWatching is the in_progress rule for shows: something to play
// next, and the viewer has started the show.
func continueWatching(rows []episodeRow) bool {
	return hasProgress(rows) && library.UpNext(episodeStates(rows)) != nil
}

func (s *Store) showSummaries(ctx context.Context, ids []int64, states map[int64][]episodeRow) (map[int64]*ShowSummary, error) {
	out := make(map[int64]*ShowSummary, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+showCols+` FROM shows WHERE id IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		show, err := scanShow(rows)
		if err != nil {
			return nil, err
		}
		out[show.ID] = buildShowSummary(show, states[show.ID])
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadShowCollections(ctx, out, args)
}

func buildShowSummary(show Show, episodes []episodeRow) *ShowSummary {
	sum := &ShowSummary{
		ID:            show.ID,
		Title:         show.Title,
		Year:          show.Year,
		Network:       show.Network,
		PosterAt:      show.PosterAt,
		BackdropAt:    show.BackdropAt,
		CreatedAt:     show.CreatedAt,
		NeedsCheck:    show.NeedsCheck,
		EpisodeCount:  len(episodes),
		HasProgress:   hasProgress(episodes),
		CollectionIDs: []int64{},
	}
	seasons := map[int]bool{}
	byItem := make(map[int64]episodeRow, len(episodes))
	for _, e := range episodes {
		byItem[e.state.ItemID] = e
		if e.createdAt > sum.LatestAddedAt {
			sum.LatestAddedAt = e.createdAt
		}
		if e.state.Season > 0 {
			seasons[e.state.Season] = true
		}
		sum.Available = sum.Available || e.state.Available
	}
	sum.SeasonCount = len(seasons)
	if next := library.UpNext(episodeStates(episodes)); next != nil {
		row := byItem[next.ItemID]
		up := &UpNextEpisode{
			ItemID:    next.ItemID,
			Season:    next.Season,
			Episode:   next.Episode,
			Title:     row.title,
			Available: next.Available,
			Progress:  row.progress,
		}
		if next.EpisodeEnd > 0 {
			end := next.EpisodeEnd
			up.EpisodeEnd = &end
		}
		sum.UpNext = up
	}
	return sum
}

func (s *Store) loadShowCollections(ctx context.Context, shows map[int64]*ShowSummary, showIDs []any) error {
	if len(shows) == 0 {
		return nil
	}
	in := placeholders(len(showIDs))
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT show_id, collection_id FROM (
			SELECT ci.show_id, ci.collection_id FROM collection_items ci
			WHERE ci.show_id IN (`+in+`)
			UNION
			SELECT mi.show_id, ci.collection_id FROM collection_items ci
			JOIN media_items mi ON mi.id = ci.item_id
			WHERE mi.deleted_at IS NULL AND mi.show_id IN (`+in+`)
		)
		ORDER BY show_id, collection_id`, append(append([]any{}, showIDs...), showIDs...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var showID, collectionID int64
		if err := rows.Scan(&showID, &collectionID); err != nil {
			return err
		}
		if sh := shows[showID]; sh != nil {
			sh.CollectionIDs = append(sh.CollectionIDs, collectionID)
		}
	}
	return rows.Err()
}
