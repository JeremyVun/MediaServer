package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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

// RemoveShowFromCollection takes the show out of the collection: its own
// row and the rows of its episodes, since either makes it a member (ruling
// B3). It returns the episode items whose rows went.
func (s *Store) RemoveShowFromCollection(ctx context.Context, collectionID, showID int64) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT ci.item_id FROM collection_items ci
		JOIN media_items mi ON mi.id = ci.item_id
		WHERE ci.collection_id = ? AND mi.show_id = ?
		ORDER BY ci.item_id`, collectionID, showID)
	if err != nil {
		return nil, err
	}
	episodes, err := collectIDs(rows)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		DELETE FROM collection_items
		WHERE collection_id = ? AND (show_id = ? OR item_id IN (
			SELECT id FROM media_items WHERE show_id = ?))`,
		collectionID, showID, showID)
	if err != nil {
		return nil, err
	}
	if err := requireRow(res); err != nil {
		return nil, err
	}
	return episodes, tx.Commit()
}

// ReorderCollection rewrites the shared sort order. Members must cover every
// row clients can see exactly once: an item member covers its own row, and a
// show member covers the show's row and the rows of its live episodes, which
// then sort together (show row first, then episode order). Naming a show and
// one of its live episodes is invalid. Hidden rows (a trashed item, a show
// with no live episodes) may be named; unnamed, each keeps its place after
// the row it follows now.
func (s *Store) ReorderCollection(ctx context.Context, collectionID int64, members []CollectionRef) error {
	if _, err := s.GetCollection(ctx, collectionID); err != nil {
		return err
	}
	rows, err := s.collectionRows(ctx, collectionID)
	if err != nil {
		return err
	}
	order, err := orderRows(rows, members)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, row := range order {
		if _, err := tx.ExecContext(ctx, `
			UPDATE collection_items SET sort_order = ?
			WHERE collection_id = ? AND (item_id = ? OR show_id = ?)`,
			i+1, collectionID, row.ItemID, row.ShowID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// collectionRow is one collection_items row; liveShow is the show of a live
// episode row, 0 otherwise. A hidden row is one the library never lists.
type collectionRow struct {
	CollectionRef
	liveShow  int64
	sortOrder int64
	hidden    bool
}

// collectionRows lists the rows in the order a show member lays them out.
func (s *Store) collectionRows(ctx context.Context, collectionID int64) ([]collectionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(ci.item_id, 0), COALESCE(ci.show_id, 0),
		       CASE WHEN mi.deleted_at IS NULL THEN COALESCE(mi.show_id, 0) ELSE 0 END,
		       ci.sort_order,
		       CASE WHEN ci.item_id IS NOT NULL THEN mi.deleted_at IS NOT NULL
		            ELSE NOT EXISTS (SELECT 1 FROM media_items e WHERE e.show_id = ci.show_id AND e.deleted_at IS NULL)
		       END
		FROM collection_items ci
		LEFT JOIN media_items mi ON mi.id = ci.item_id
		WHERE ci.collection_id = ?
		ORDER BY ci.show_id IS NULL, COALESCE(mi.season, 1) = 0, COALESCE(mi.season, 1),
		         COALESCE(mi.episode, 0), ci.item_id, ci.show_id`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []collectionRow
	for rows.Next() {
		var r collectionRow
		if err := rows.Scan(&r.ItemID, &r.ShowID, &r.liveShow, &r.sortOrder, &r.hidden); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func orderRows(rows []collectionRow, members []CollectionRef) ([]CollectionRef, error) {
	invalid := func(why string) error {
		return fmt.Errorf("members must match current collection members (%s): %w", why, ErrInvalidInput)
	}
	shows := map[int64]bool{}
	for _, m := range members {
		if !m.valid() {
			return nil, invalid("each member names one item or one show")
		}
		if m.ShowID > 0 {
			if shows[m.ShowID] {
				return nil, invalid("a show is listed twice")
			}
			shows[m.ShowID] = true
		}
	}
	byItem := map[int64]collectionRow{}
	for _, r := range rows {
		if r.ItemID > 0 {
			byItem[r.ItemID] = r
		}
	}
	used := map[CollectionRef]bool{}
	order := make([]CollectionRef, 0, len(rows))
	for _, m := range members {
		if m.ItemID > 0 {
			r, ok := byItem[m.ItemID]
			switch {
			case !ok:
				return nil, invalid("an item is not in the collection")
			case used[r.CollectionRef]:
				return nil, invalid("an item is listed twice")
			case shows[r.liveShow]:
				return nil, invalid("an episode is listed with its show")
			}
			used[r.CollectionRef] = true
			order = append(order, r.CollectionRef)
			continue
		}
		covered := false
		for _, r := range rows {
			if r.ShowID == m.ShowID || r.liveShow == m.ShowID {
				used[r.CollectionRef] = true
				order = append(order, r.CollectionRef)
				covered = true
			}
		}
		if !covered {
			return nil, invalid("a show is not in the collection")
		}
	}
	for _, r := range rows {
		if !used[r.CollectionRef] && !r.hidden {
			return nil, invalid("a member is missing")
		}
	}
	return keepHiddenInPlace(rows, order, used), nil
}

// keepHiddenInPlace slots each unnamed row straight after the row it follows
// now, so a trashed item comes back from the trash where it was.
func keepHiddenInPlace(rows []collectionRow, order []CollectionRef, used map[CollectionRef]bool) []CollectionRef {
	if len(order) == len(rows) {
		return order
	}
	current := slices.Clone(rows)
	slices.SortStableFunc(current, func(a, b collectionRow) int { return cmp.Compare(a.sortOrder, b.sortOrder) })
	after := map[CollectionRef][]CollectionRef{}
	var anchor CollectionRef
	for _, r := range current {
		if used[r.CollectionRef] {
			anchor = r.CollectionRef
			continue
		}
		after[anchor] = append(after[anchor], r.CollectionRef)
	}
	out := slices.Clone(after[CollectionRef{}])
	for _, ref := range order {
		out = append(out, ref)
		out = append(out, after[ref]...)
	}
	return out
}
