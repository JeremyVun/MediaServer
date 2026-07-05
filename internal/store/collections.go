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

func (s *Store) ListCollections(ctx context.Context) ([]CollectionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.created_at, COUNT(mi.id) AS item_count
		FROM collections c
		LEFT JOIN collection_items ci ON ci.collection_id = c.id
		LEFT JOIN media_items mi ON mi.id = ci.item_id AND mi.deleted_at IS NULL
		GROUP BY c.id
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
		SELECT collection_id, item_id
		FROM (
			SELECT ci.collection_id, ci.item_id,
			       ROW_NUMBER() OVER (PARTITION BY ci.collection_id ORDER BY ci.sort_order, ci.item_id) AS rn
			FROM collection_items ci
			JOIN media_items mi ON mi.id = ci.item_id
			WHERE mi.deleted_at IS NULL
			  AND ci.collection_id IN (`+placeholders(len(args))+`)
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
		ON CONFLICT(collection_id, item_id) DO NOTHING`,
		collectionID, itemID, collectionID)
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

func (s *Store) ReorderCollection(ctx context.Context, collectionID int64, itemIDs []int64) error {
	if _, err := s.GetCollection(ctx, collectionID); err != nil {
		return err
	}
	current, err := s.collectionItemIDs(ctx, collectionID)
	if err != nil {
		return err
	}
	if !sameInt64Set(current, itemIDs) {
		return fmt.Errorf("item_ids must match current collection items: %w", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, itemID := range itemIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE collection_items SET sort_order = ?
			WHERE collection_id = ? AND item_id = ?`,
			i+1, collectionID, itemID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) collectionItemIDs(ctx context.Context, collectionID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT item_id
		FROM collection_items
		WHERE collection_id = ?
		ORDER BY sort_order, item_id`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func sameInt64Set(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[int64]int, len(a))
	for _, id := range a {
		counts[id]++
	}
	for _, id := range b {
		if counts[id] == 0 {
			return false
		}
		counts[id]--
	}
	return true
}
