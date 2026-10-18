package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Collection groups items for the library's filter chips and collection
// screens.
type Collection struct {
	ID        int64
	Name      string
	CreatedAt string
}

type CollectionSummary struct {
	Collection
	ItemCount    int
	ThumbItemIDs []int64
}

func (s *Store) CreateCollection(ctx context.Context, name string) (Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Collection{}, fmt.Errorf("collection name is required: %w", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO collections (name) VALUES (?)`, name)
	if err != nil {
		return Collection{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Collection{}, err
	}
	return s.GetCollection(ctx, id)
}

// ListCollections counts live item members plus show members that still
// have a live episode. A show member's thumbnail is its first episode.
func (s *Store) ListCollections(ctx context.Context) ([]CollectionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.created_at,
		       (SELECT COUNT(*) FROM collection_items ci
		        JOIN media_items mi ON mi.id = ci.item_id
		        WHERE ci.collection_id = c.id AND mi.deleted_at IS NULL)
		     + (SELECT COUNT(*) FROM collection_items ci
		        WHERE ci.collection_id = c.id AND ci.show_id IS NOT NULL
		          AND EXISTS (SELECT 1 FROM media_items mi WHERE mi.show_id = ci.show_id AND mi.deleted_at IS NULL))
		       AS item_count
		FROM collections c
		ORDER BY c.name COLLATE NOCASE, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	collections := []CollectionSummary{}
	for rows.Next() {
		var c CollectionSummary
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt, &c.ItemCount); err != nil {
			return nil, err
		}
		c.ThumbItemIDs = []int64{}
		collections = append(collections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(collections) == 0 {
		return collections, nil
	}

	byID := make(map[int64]int, len(collections))
	args := make([]any, len(collections))
	for i := range collections {
		byID[collections[i].ID] = i
		args[i] = collections[i].ID
	}
	thumbRows, err := s.db.QueryContext(ctx, `
		SELECT collection_id, thumb_item_id
		FROM (
			SELECT collection_id, thumb_item_id,
			       ROW_NUMBER() OVER (PARTITION BY collection_id ORDER BY sort_order, item_id, show_id) AS rn
			FROM (
				SELECT ci.collection_id, ci.sort_order, ci.item_id, ci.show_id,
				       CASE WHEN ci.item_id IS NOT NULL
				            THEN (SELECT mi.id FROM media_items mi WHERE mi.id = ci.item_id AND mi.deleted_at IS NULL)
				            ELSE (`+firstEpisodeSQL+`)
				       END AS thumb_item_id
				FROM collection_items ci
				WHERE ci.collection_id IN (`+placeholders(len(args))+`)
			)
			WHERE thumb_item_id IS NOT NULL
		)
		WHERE rn <= 4
		ORDER BY collection_id, rn`, args...)
	if err != nil {
		return nil, err
	}
	defer thumbRows.Close()
	for thumbRows.Next() {
		var collectionID, itemID int64
		if err := thumbRows.Scan(&collectionID, &itemID); err != nil {
			return nil, err
		}
		if idx, ok := byID[collectionID]; ok {
			collections[idx].ThumbItemIDs = append(collections[idx].ThumbItemIDs, itemID)
		}
	}
	return collections, thumbRows.Err()
}

// firstEpisodeSQL picks the live episode a show member is pictured by,
// correlated on ci.show_id: main sequence first, then specials.
const firstEpisodeSQL = `SELECT mi.id FROM media_items mi
	WHERE mi.show_id = ci.show_id AND mi.deleted_at IS NULL
	ORDER BY COALESCE(mi.season, 1) = 0, COALESCE(mi.season, 1), COALESCE(mi.episode, 0), mi.id
	LIMIT 1`

func (s *Store) GetCollection(ctx context.Context, id int64) (Collection, error) {
	var c Collection
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, created_at FROM collections WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Collection{}, ErrNotFound
	}
	if err != nil {
		return Collection{}, err
	}
	return c, nil
}

func (s *Store) UpdateCollection(ctx context.Context, id int64, name string) (Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Collection{}, fmt.Errorf("collection name is required: %w", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE collections SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return Collection{}, err
	}
	if err := requireRow(res); err != nil {
		return Collection{}, err
	}
	return s.GetCollection(ctx, id)
}

func (s *Store) DeleteCollection(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// CollectionRef names one collection member: exactly one of ItemID and
// ShowID is non-zero.
type CollectionRef struct {
	ItemID int64
	ShowID int64
}

func (r CollectionRef) valid() bool { return (r.ItemID > 0) != (r.ShowID > 0) }

// AddItemToCollection appends the item at the end of the collection's sort
// order; adding an item twice is a no-op.
func (s *Store) AddItemToCollection(ctx context.Context, collectionID, itemID int64) error {
	if _, err := s.GetCollection(ctx, collectionID); err != nil {
		return err
	}
	item, err := s.GetItem(ctx, itemID)
	if err != nil {
		return err
	}
	if item.DeletedAt != nil {
		return fmt.Errorf("item is trashed: %w", ErrInvalidInput)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO collection_items (collection_id, item_id, sort_order)
		VALUES (?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1
		               FROM collection_items WHERE collection_id = ?))
		ON CONFLICT(collection_id, item_id) WHERE item_id IS NOT NULL DO NOTHING`,
		collectionID, itemID, collectionID)
	return err
}

// AddShowToCollection appends the whole show, sharing the collection's one
// sort order with its items; adding a show twice is a no-op.
func (s *Store) AddShowToCollection(ctx context.Context, collectionID, showID int64) error {
	if _, err := s.GetCollection(ctx, collectionID); err != nil {
		return err
	}
	if _, err := s.GetShow(ctx, showID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO collection_items (collection_id, show_id, sort_order)
		VALUES (?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1
		               FROM collection_items WHERE collection_id = ?))
		ON CONFLICT(collection_id, show_id) WHERE show_id IS NOT NULL DO NOTHING`,
		collectionID, showID, collectionID)
	return err
}

func (s *Store) RemoveItemFromCollection(ctx context.Context, collectionID, itemID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM collection_items WHERE collection_id = ? AND item_id = ?`,
		collectionID, itemID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (s *Store) RemoveShowFromCollection(ctx context.Context, collectionID, showID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM collection_items WHERE collection_id = ? AND show_id = ?`,
		collectionID, showID)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ReorderCollection rewrites the shared sort order; members must list every
// current item and show exactly once.
func (s *Store) ReorderCollection(ctx context.Context, collectionID int64, members []CollectionRef) error {
	if _, err := s.GetCollection(ctx, collectionID); err != nil {
		return err
	}
	current, err := s.collectionRefs(ctx, collectionID)
	if err != nil {
		return err
	}
	if !sameRefSet(current, members) {
		return fmt.Errorf("members must match current collection members: %w", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, m := range members {
		if _, err := tx.ExecContext(ctx, `
			UPDATE collection_items SET sort_order = ?
			WHERE collection_id = ? AND (item_id = ? OR show_id = ?)`,
			i+1, collectionID, m.ItemID, m.ShowID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) collectionRefs(ctx context.Context, collectionID int64) ([]CollectionRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(item_id, 0), COALESCE(show_id, 0)
		FROM collection_items
		WHERE collection_id = ?
		ORDER BY sort_order, item_id, show_id`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []CollectionRef
	for rows.Next() {
		var r CollectionRef
		if err := rows.Scan(&r.ItemID, &r.ShowID); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

func sameRefSet(a, b []CollectionRef) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[CollectionRef]int, len(a))
	for _, r := range a {
		counts[r]++
	}
	for _, r := range b {
		if !r.valid() || counts[r] == 0 {
			return false
		}
		counts[r]--
	}
	return true
}
