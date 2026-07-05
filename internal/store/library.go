package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

type Progress struct {
	PositionS float64
	DurationS float64
	Completed bool
}

type ItemSummary struct {
	ID            int64
	Type          string
	Title         string
	Year          *int
	DurationS     *float64
	CreatedAt     string // SQLite datetime text, UTC — drives the client's "New" badge
	Available     bool
	Progress      *Progress
	CollectionIDs []int64
	FileIDs       []int64 // ascending, matching ListFilesForItem — the thumb handler's resolution order
}

type SearchItemsOpts struct {
	Query        string
	Type         string
	CollectionID int64
	Uncollected  bool // filter to items in no collection (ignored when CollectionID > 0)
	Limit        int
}

func (s *Store) ListItemSummaries(ctx context.Context, opts ListItemsOpts) ([]ItemSummary, int, error) {
	where := "mi.deleted_at IS NULL"
	if opts.Trashed {
		where = "mi.deleted_at IS NOT NULL"
	}
	join := ""
	args := []any{}
	if opts.Type != "" {
		where += " AND mi.type = ?"
		args = append(args, opts.Type)
	}
	if opts.CollectionID > 0 {
		join = " JOIN collection_items ci ON ci.item_id = mi.id"
		where += " AND ci.collection_id = ?"
		args = append(args, opts.CollectionID)
	} else if opts.Uncollected {
		where += " AND NOT EXISTS (SELECT 1 FROM collection_items ci WHERE ci.item_id = mi.id)"
	}
	if opts.InProgress {
		where += " AND EXISTS (SELECT 1 FROM watch_progress wpf WHERE wpf.item_id = mi.id AND wpf.position_s > 0 AND wpf.completed = 0)"
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM media_items mi"+join+" WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderCol, defaultOrder := "mi.created_at", "DESC"
	switch opts.Sort {
	case "", "added":
		if opts.Sort == "" && opts.CollectionID > 0 {
			orderCol, defaultOrder = "ci.sort_order", "ASC"
		}
	case "title":
		orderCol, defaultOrder = "mi.title COLLATE NOCASE", "ASC"
	case "year":
		orderCol, defaultOrder = "mi.year", "ASC"
	case "watched":
		orderCol, defaultOrder = "wp.updated_at", "DESC"
	default:
		return nil, 0, fmt.Errorf("bad sort %q: %w", opts.Sort, ErrInvalidInput)
	}
	dir := defaultOrder
	switch strings.ToLower(opts.Order) {
	case "":
	case "asc":
		dir = "ASC"
	case "desc":
		dir = "DESC"
	default:
		return nil, 0, fmt.Errorf("bad order %q: %w", opts.Order, ErrInvalidInput)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 60
	}
	if limit > 500 {
		limit = 500
	}
	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(summarySelect()+`
		%s
		WHERE %s
		GROUP BY mi.id
		ORDER BY %s %s, mi.id %s
		LIMIT ? OFFSET ?`, join, where, orderCol, dir, dir), args...)
	if err != nil {
		return nil, 0, err
	}
	items, err := scanSummaries(rows)
	if err != nil {
		return nil, 0, err
	}
	if err := s.loadSummaryRelations(ctx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Store) SearchItemSummaries(ctx context.Context, opts SearchItemsOpts) ([]ItemSummary, int, error) {
	match := FTS5PrefixQuery(opts.Query)
	if match == "" {
		return []ItemSummary{}, 0, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	where := "mi.deleted_at IS NULL"
	args := []any{match}
	if opts.Type != "" {
		where += " AND mi.type = ?"
		args = append(args, opts.Type)
	}
	if opts.CollectionID > 0 {
		where += " AND mi.id IN (SELECT item_id FROM collection_items WHERE collection_id = ?)"
		args = append(args, opts.CollectionID)
	} else if opts.Uncollected {
		where += " AND mi.id NOT IN (SELECT item_id FROM collection_items)"
	}

	var total int
	countArgs := append([]any{}, args...)
	if err := s.db.QueryRowContext(ctx, `
		WITH matched(rowid) AS (
			SELECT rowid FROM items_fts WHERE items_fts MATCH ?
		)
		SELECT COUNT(*)
		FROM matched
		JOIN media_items mi ON mi.id = matched.rowid
		WHERE `+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		WITH matched(rowid) AS (
			SELECT rowid FROM items_fts WHERE items_fts MATCH ?
		)
		`+summarySelect()+`
		JOIN matched ON matched.rowid = mi.id
		WHERE `+where+`
		GROUP BY mi.id
		ORDER BY mi.title COLLATE NOCASE, mi.id
		LIMIT ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	items, err := scanSummaries(rows)
	if err != nil {
		return nil, 0, err
	}
	if err := s.loadSummaryRelations(ctx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetItemSummary loads one item in the list-endpoint shape — used to
// hydrate item.added/item.updated bus events (SPEC-API's SSE contract sends
// the full item object, so the hub must never publish bare ids).
func (s *Store) GetItemSummary(ctx context.Context, id int64) (ItemSummary, error) {
	rows, err := s.db.QueryContext(ctx, summarySelect()+`
		WHERE mi.id = ?
		GROUP BY mi.id`, id)
	if err != nil {
		return ItemSummary{}, err
	}
	items, err := scanSummaries(rows)
	if err != nil {
		return ItemSummary{}, err
	}
	if len(items) == 0 {
		return ItemSummary{}, ErrNotFound
	}
	if err := s.loadSummaryRelations(ctx, items); err != nil {
		return ItemSummary{}, err
	}
	return items[0], nil
}

func FTS5PrefixQuery(q string) string {
	var terms []string
	for _, raw := range strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		raw = strings.ToLower(strings.TrimSpace(raw))
		if raw == "" {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(raw, `"`, `""`)+`"*`)
	}
	return strings.Join(terms, " ")
}

func summarySelect() string {
	return `
		SELECT mi.id, mi.type, mi.title, mi.year, mi.created_at,
		       MAX(mf.duration_s) AS duration_s,
		       CASE WHEN SUM(CASE WHEN mf.status = 'online' AND lr.online = 1 THEN 1 ELSE 0 END) > 0 THEN 1 ELSE 0 END AS available,
		       wp.position_s, wp.duration_s, wp.completed
		FROM media_items mi
		LEFT JOIN media_files mf ON mf.item_id = mi.id
		LEFT JOIN library_roots lr ON lr.id = mf.root_id
		LEFT JOIN watch_progress wp ON wp.item_id = mi.id`
}

func scanSummaries(rows *sql.Rows) ([]ItemSummary, error) {
	defer rows.Close()
	var items []ItemSummary
	for rows.Next() {
		var it ItemSummary
		var year sql.NullInt64
		var duration sql.NullFloat64
		var available int
		var progressPos, progressDur sql.NullFloat64
		var completed sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Type, &it.Title, &year, &it.CreatedAt, &duration, &available,
			&progressPos, &progressDur, &completed); err != nil {
			return nil, err
		}
		if year.Valid {
			y := int(year.Int64)
			it.Year = &y
		}
		if duration.Valid {
			it.DurationS = &duration.Float64
		}
		it.Available = available != 0
		if progressPos.Valid {
			it.Progress = &Progress{
				PositionS: progressPos.Float64,
				DurationS: progressDur.Float64,
				Completed: completed.Valid && completed.Int64 != 0,
			}
		}
		it.CollectionIDs = []int64{}
		items = append(items, it)
	}
	return items, rows.Err()
}

// loadSummaryRelations hydrates the per-item id lists (collections, files),
// one batched query each — never per item.
func (s *Store) loadSummaryRelations(ctx context.Context, items []ItemSummary) error {
	if err := s.loadSummaryCollections(ctx, items); err != nil {
		return err
	}
	return s.loadSummaryFileIDs(ctx, items)
}

func (s *Store) loadSummaryFileIDs(ctx context.Context, items []ItemSummary) error {
	if len(items) == 0 {
		return nil
	}
	byID := make(map[int64]int, len(items))
	args := make([]any, len(items))
	for i := range items {
		byID[items[i].ID] = i
		args[i] = items[i].ID
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT item_id, id
		FROM media_files
		WHERE item_id IN (`+placeholders(len(args))+`)
		ORDER BY item_id, id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID, fileID int64
		if err := rows.Scan(&itemID, &fileID); err != nil {
			return err
		}
		if idx, ok := byID[itemID]; ok {
			items[idx].FileIDs = append(items[idx].FileIDs, fileID)
		}
	}
	return rows.Err()
}

func (s *Store) loadSummaryCollections(ctx context.Context, items []ItemSummary) error {
	if len(items) == 0 {
		return nil
	}
	byID := make(map[int64]int, len(items))
	args := make([]any, len(items))
	for i := range items {
		byID[items[i].ID] = i
		args[i] = items[i].ID
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT item_id, collection_id
		FROM collection_items
		WHERE item_id IN (`+placeholders(len(args))+`)
		ORDER BY item_id, sort_order`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID, collectionID int64
		if err := rows.Scan(&itemID, &collectionID); err != nil {
			return err
		}
		if idx, ok := byID[itemID]; ok {
			items[idx].CollectionIDs = append(items[idx].CollectionIDs, collectionID)
		}
	}
	return rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}
