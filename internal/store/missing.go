package store

import (
	"context"
	"database/sql"
	"time"
)

// RootFileCounts counts a root's files that back live (untrashed) items.
type RootFileCounts struct {
	Live    int
	Missing int
}

// CountLiveFilesByRoot feeds the mass-vanish guard of build ruling B16.
func (s *Store) CountLiveFilesByRoot(ctx context.Context) (map[int64]RootFileCounts, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.root_id, COUNT(*), SUM(f.status = 'missing')
		FROM media_files f JOIN media_items i ON i.id = f.item_id
		WHERE i.deleted_at IS NULL
		GROUP BY f.root_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[int64]RootFileCounts{}
	for rows.Next() {
		var rootID int64
		var c RootFileCounts
		if err := rows.Scan(&rootID, &c.Live, &c.Missing); err != nil {
			return nil, err
		}
		counts[rootID] = c
	}
	return counts, rows.Err()
}

// MissingItem is a live item every one of whose files has been missing since
// the cutoff or earlier.
type MissingItem struct {
	ID     int64
	Title  string
	ShowID *int64
}

const missingSinceCutoff = `
	EXISTS (SELECT 1 FROM media_files f WHERE f.item_id = mi.id)
	AND NOT EXISTS (SELECT 1 FROM media_files f WHERE f.item_id = mi.id
		AND (f.status <> 'missing' OR f.missing_since IS NULL OR f.missing_since > ?))`

func (s *Store) ListItemsMissingSince(ctx context.Context, cutoff time.Time) ([]MissingItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mi.id, mi.title, mi.show_id FROM media_items mi
		WHERE mi.deleted_at IS NULL AND `+missingSinceCutoff+`
		ORDER BY mi.id`, FormatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MissingItem{}
	for rows.Next() {
		var it MissingItem
		var showID sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Title, &showID); err != nil {
			return nil, err
		}
		it.ShowID = int64Ptr(showID)
		items = append(items, it)
	}
	return items, rows.Err()
}

// TrashMissingItem moves an item to Trash only if it is still live and all
// its files are still missing since the cutoff, so a file re-paired since the
// caller looked keeps its item. File rows stay missing: nothing went to
// .trash. Reports whether the item was trashed.
func (s *Store) TrashMissingItem(ctx context.Context, id int64, cutoff time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_items AS mi SET deleted_at = datetime('now'), updated_at = datetime('now')
		WHERE mi.id = ? AND mi.deleted_at IS NULL AND `+missingSinceCutoff,
		id, FormatTime(cutoff))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// NextMissingSince returns the earliest missing_since after the given time
// among live items' files, so the sweep can wake when that grace runs out.
func (s *Store) NextMissingSince(ctx context.Context, after time.Time) (time.Time, bool, error) {
	var next sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(f.missing_since)
		FROM media_files f JOIN media_items i ON i.id = f.item_id
		WHERE i.deleted_at IS NULL AND f.status = 'missing' AND f.missing_since > ?`,
		FormatTime(after)).Scan(&next)
	if err != nil || !next.Valid {
		return time.Time{}, false, err
	}
	t, err := ParseTime(next.String)
	return t, err == nil, err
}
