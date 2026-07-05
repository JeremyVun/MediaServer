package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Item is a logical library entry (one row per video/movie/episode); the
// bytes live in media_files rows attached to it.
type Item struct {
	ID        int64
	Type      string // video|movie|episode
	Title     string
	Year      *int
	Summary   *string
	CreatedAt string  // SQLite datetime text, UTC
	UpdatedAt string  //
	DeletedAt *string // set when trashed
}

type NewItem struct {
	Type    string // defaults to 'video'
	Title   string
	Year    *int
	Summary *string
}

func (s *Store) CreateItem(ctx context.Context, in NewItem) (Item, error) {
	if in.Type == "" {
		in.Type = "video"
	}
	if in.Title == "" {
		return Item{}, fmt.Errorf("item title is required")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO media_items (type, title, year, summary) VALUES (?, ?, ?, ?)`,
		in.Type, in.Title, in.Year, in.Summary)
	if err != nil {
		return Item{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Item{}, err
	}
	return s.GetItem(ctx, id)
}

// CreateItemWithFile inserts a new item and its first backing file in one
// transaction — ingest can never leave an orphan media_items row if the
// file insert fails (e.g. a concurrent probe won the UNIQUE(root_id,
// rel_path) race). in.File.ItemID is ignored; the new item's id is used.
func (s *Store) CreateItemWithFile(ctx context.Context, item NewItem, file NewFile) (Item, File, error) {
	if item.Type == "" {
		item.Type = "video"
	}
	if item.Title == "" {
		return Item{}, File{}, fmt.Errorf("item title is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, File{}, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO media_items (type, title, year, summary) VALUES (?, ?, ?, ?)`,
		item.Type, item.Title, item.Year, item.Summary)
	if err != nil {
		return Item{}, File{}, err
	}
	itemID, err := res.LastInsertId()
	if err != nil {
		return Item{}, File{}, err
	}
	res, err = tx.ExecContext(ctx, `
		INSERT INTO media_files (item_id, root_id, rel_path, size, mtime, fingerprint)
		VALUES (?, ?, ?, ?, ?, ?)`,
		itemID, file.RootID, file.RelPath, file.Size, FormatTime(file.Mtime), file.Fingerprint)
	if err != nil {
		return Item{}, File{}, err
	}
	fileID, err := res.LastInsertId()
	if err != nil {
		return Item{}, File{}, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, File{}, err
	}

	created, err := s.GetItem(ctx, itemID)
	if err != nil {
		return Item{}, File{}, err
	}
	createdFile, err := s.GetFile(ctx, fileID)
	if err != nil {
		return Item{}, File{}, err
	}
	return created, createdFile, nil
}

func (s *Store) GetItem(ctx context.Context, id int64) (Item, error) {
	return scanItem(s.db.QueryRowContext(ctx, `
		SELECT id, type, title, year, summary, created_at, updated_at, deleted_at
		FROM media_items WHERE id = ?`, id))
}

// UpdateItemParams applies partial edits: nil fields are left unchanged.
type UpdateItemParams struct {
	Type    *string
	Title   *string
	Year    *int
	Summary *string
}

func (s *Store) UpdateItem(ctx context.Context, id int64, p UpdateItemParams) (Item, error) {
	sets := []string{"updated_at = datetime('now')"}
	args := []any{}
	if p.Type != nil {
		sets = append(sets, "type = ?")
		args = append(args, *p.Type)
	}
	if p.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *p.Title)
	}
	if p.Year != nil {
		sets = append(sets, "year = ?")
		args = append(args, *p.Year)
	}
	if p.Summary != nil {
		sets = append(sets, "summary = ?")
		args = append(args, *p.Summary)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		"UPDATE media_items SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return Item{}, err
	}
	if err := requireRow(res); err != nil {
		return Item{}, err
	}
	return s.GetItem(ctx, id)
}

// SoftDeleteItem marks an item trashed. File moves to .trash are the
// caller's job (HTTP/library service). The DB state change itself is
// transactional across the item and its files.
func (s *Store) SoftDeleteItem(ctx context.Context, id int64) error {
	return s.MarkItemTrashed(ctx, id)
}

// RestoreItem undoes a soft delete.
func (s *Store) RestoreItem(ctx context.Context, id int64) error {
	return s.MarkItemRestored(ctx, id)
}

func (s *Store) MarkItemTrashed(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE media_items SET deleted_at = datetime('now'), updated_at = datetime('now')
		WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if err := requireRow(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_files SET status = 'trashed' WHERE item_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkItemRestored(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE media_items SET deleted_at = NULL, updated_at = datetime('now')
		WHERE id = ? AND deleted_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if err := requireRow(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE media_files SET status = 'online'
		WHERE item_id = ? AND status = 'trashed'`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// HardDeleteItem removes the row (cascades to files/streams). Used by the
// trash purge job only.
func (s *Store) HardDeleteItem(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM media_items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (s *Store) ListTrashedItemsBefore(ctx context.Context, cutoff time.Time, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, type, title, year, summary, created_at, updated_at, deleted_at
		FROM media_items
		WHERE deleted_at IS NOT NULL AND deleted_at <= ?
		ORDER BY deleted_at, id
		LIMIT ?`, FormatTime(cutoff), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListItemsOpts mirrors GET /api/items query parameters.
type ListItemsOpts struct {
	Sort         string // added|title|year|watched (default added)
	Order        string // asc|desc (default desc for added/watched, asc otherwise)
	Type         string // filter; empty = all
	CollectionID int64  // filter to one collection's items; 0 = all
	Uncollected  bool   // filter to items in no collection (ignored when CollectionID > 0)
	Trashed      bool   // list trashed instead of live items
	InProgress   bool   // filter to items with started, not-completed watch progress
	Offset       int
	Limit        int // <=0 means default 60, capped at 500
}

func scanItem(row rowScanner) (Item, error) {
	var it Item
	var year sql.NullInt64
	var summary, deletedAt sql.NullString
	err := row.Scan(&it.ID, &it.Type, &it.Title, &year, &summary,
		&it.CreatedAt, &it.UpdatedAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	if year.Valid {
		y := int(year.Int64)
		it.Year = &y
	}
	if summary.Valid {
		it.Summary = &summary.String
	}
	if deletedAt.Valid {
		it.DeletedAt = &deletedAt.String
	}
	return it, nil
}
