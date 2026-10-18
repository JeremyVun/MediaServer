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
	ID           int64
	Type         string // video|movie|episode
	Title        string
	Year         *int
	Summary      *string
	CreatedAt    string  // SQLite datetime text, UTC
	UpdatedAt    string  //
	DeletedAt    *string // set when trashed
	ShowID       *int64
	Season       *int
	Episode      *int
	EpisodeEnd   *int    // nil unless the file spans several episodes
	ShowKey      *string // parsed show key, kept so edits can teach aliases
	NeedsCheck   bool
	Locked       bool
	ClassifiedAt *string // nil while classify is pending
}

// NewItem is the parser's guess at creation; the item starts unclassified,
// so it may be an episode without a show until classify files it.
type NewItem struct {
	Type       string // defaults to 'video'
	Title      string
	Year       *int
	Summary    *string
	ShowKey    *string
	Season     *int
	Episode    *int
	EpisodeEnd *int
}

const itemCols = `id, type, title, year, summary, created_at, updated_at, deleted_at,
	show_id, season, episode, episode_end, show_key, needs_check, locked, classified_at`

const insertItemSQL = `INSERT INTO media_items (type, title, year, summary, show_key, season, episode, episode_end)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

func (in NewItem) insertArgs() []any {
	return []any{in.Type, in.Title, in.Year, in.Summary, in.ShowKey, in.Season, in.Episode, in.EpisodeEnd}
}

func (s *Store) CreateItem(ctx context.Context, in NewItem) (Item, error) {
	if in.Type == "" {
		in.Type = "video"
	}
	if in.Title == "" {
		return Item{}, fmt.Errorf("item title is required")
	}
	res, err := s.db.ExecContext(ctx, insertItemSQL, in.insertArgs()...)
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

	res, err := tx.ExecContext(ctx, insertItemSQL, item.insertArgs()...)
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
	return scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemCols+` FROM media_items WHERE id = ?`, id))
}

// UpdateItemParams applies partial edits: nil or unset fields are left
// unchanged.
type UpdateItemParams struct {
	Type         *string
	Title        *string
	Year         *int
	Summary      *string
	ShowID       Nullable[int64]
	Season       Nullable[int]
	Episode      Nullable[int]
	EpisodeEnd   Nullable[int]
	ShowKey      Nullable[string]
	NeedsCheck   *bool
	Locked       *bool
	ClassifiedAt Nullable[string]
}

// UpdateItem rejects with ErrInvalidInput an edit that would leave a
// classified item breaking the episode invariant (see
// checkEpisodeInvariant), or that names a show that doesn't exist.
func (s *Store) UpdateItem(ctx context.Context, id int64, p UpdateItemParams) (Item, error) {
	sets := []string{"updated_at = datetime('now')"}
	args := []any{}
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if p.Type != nil {
		set("type", *p.Type)
	}
	if p.Title != nil {
		set("title", *p.Title)
	}
	if p.Year != nil {
		set("year", *p.Year)
	}
	if p.Summary != nil {
		set("summary", *p.Summary)
	}
	if p.ShowID.Set {
		set("show_id", nullableArg(p.ShowID))
	}
	if p.Season.Set {
		set("season", nullableArg(p.Season))
	}
	if p.Episode.Set {
		set("episode", nullableArg(p.Episode))
	}
	if p.EpisodeEnd.Set {
		set("episode_end", nullableArg(p.EpisodeEnd))
	}
	if p.ShowKey.Set {
		set("show_key", nullableArg(p.ShowKey))
	}
	if p.NeedsCheck != nil {
		set("needs_check", boolToInt(*p.NeedsCheck))
	}
	if p.Locked != nil {
		set("locked", boolToInt(*p.Locked))
	}
	if p.ClassifiedAt.Set {
		set("classified_at", nullableArg(p.ClassifiedAt))
	}
	args = append(args, id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	if p.ShowID.Value != nil {
		if err := requireShow(ctx, tx, *p.ShowID.Value); err != nil {
			return Item{}, err
		}
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE media_items SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return Item{}, err
	}
	if err := requireRow(res); err != nil {
		return Item{}, err
	}
	if err := checkEpisodeInvariant(ctx, tx, id); err != nil {
		return Item{}, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	return s.GetItem(ctx, id)
}

const invariantViolation = `classified_at IS NOT NULL AND ((type = 'episode') <> (show_id IS NOT NULL))`

// checkEpisodeInvariant enforces build ruling B1 on the given items: once
// classified, an item is an episode exactly when it belongs to a show.
// Pending items (classified_at NULL) are exempt.
func checkEpisodeInvariant(ctx context.Context, q queryer, itemIDs ...int64) error {
	if len(itemIDs) == 0 {
		return nil
	}
	args := make([]any, len(itemIDs))
	for i, id := range itemIDs {
		args[i] = id
	}
	var bad int64
	err := q.QueryRowContext(ctx, `
		SELECT id FROM media_items
		WHERE id IN (`+placeholders(len(args))+`) AND `+invariantViolation+`
		LIMIT 1`, args...).Scan(&bad)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("item %d: a classified item is an episode exactly when it has a show: %w", bad, ErrInvalidInput)
}

// EpisodeInvariantViolations lists every item breaking the B1 invariant;
// it should always be empty.
func (s *Store) EpisodeInvariantViolations(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM media_items WHERE `+invariantViolation+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

func collectIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
// trash purge job only. When it was its show's last item the show and its
// aliases go too, and the removed show is returned so the caller can delete
// its cached poster and backdrop files.
func (s *Store) HardDeleteItem(ctx context.Context, id int64) (*Show, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var showID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT show_id FROM media_items WHERE id = ?`, id).Scan(&showID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_items WHERE id = ?`, id); err != nil {
		return nil, err
	}
	var removed *Show
	if showID.Valid {
		var remaining int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM media_items WHERE show_id = ?`, showID.Int64).Scan(&remaining); err != nil {
			return nil, err
		}
		if remaining == 0 {
			show, err := getShow(ctx, tx, showID.Int64)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM shows WHERE id = ?`, show.ID); err != nil {
				return nil, err
			}
			removed = &show
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return removed, nil
}

func (s *Store) ListTrashedItemsBefore(ctx context.Context, cutoff time.Time, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+itemCols+`
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
	var year, showID, season, episode, episodeEnd sql.NullInt64
	var summary, deletedAt, showKey, classifiedAt sql.NullString
	var needsCheck, locked int
	err := row.Scan(&it.ID, &it.Type, &it.Title, &year, &summary,
		&it.CreatedAt, &it.UpdatedAt, &deletedAt,
		&showID, &season, &episode, &episodeEnd, &showKey, &needsCheck, &locked, &classifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	it.Year = intPtr(year)
	it.Summary = stringPtr(summary)
	it.DeletedAt = stringPtr(deletedAt)
	it.ShowID = int64Ptr(showID)
	it.Season = intPtr(season)
	it.Episode = intPtr(episode)
	it.EpisodeEnd = intPtr(episodeEnd)
	it.ShowKey = stringPtr(showKey)
	it.NeedsCheck = needsCheck != 0
	it.Locked = locked != 0
	it.ClassifiedAt = stringPtr(classifiedAt)
	return it, nil
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func stringPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}
