package store

import (
	"context"
	"database/sql"
	"errors"
)

// Root is a library root: a directory on some volume whose contents form
// part of the one flat library.
type Root struct {
	ID        int64
	Name      string
	Path      string
	Online    bool
	Attached  bool
	CreatedAt string // SQLite datetime text, UTC
}

// UpsertRoot inserts a root by path or updates its name if it already
// exists. Used at boot to seed roots from config (DB stays source of truth).
func (s *Store) UpsertRoot(ctx context.Context, name, path string) (Root, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_roots (name, path, attached) VALUES (?, ?, 1)
		ON CONFLICT(path) DO UPDATE SET name = excluded.name, attached = 1`,
		name, path)
	if err != nil {
		return Root{}, err
	}
	return s.GetRootByPath(ctx, path)
}

func (s *Store) GetRoot(ctx context.Context, id int64) (Root, error) {
	return s.scanRoot(s.db.QueryRowContext(ctx,
		`SELECT id, name, path, online, attached, created_at FROM library_roots WHERE id = ?`, id))
}

func (s *Store) GetRootByPath(ctx context.Context, path string) (Root, error) {
	return s.scanRoot(s.db.QueryRowContext(ctx,
		`SELECT id, name, path, online, attached, created_at FROM library_roots WHERE path = ?`, path))
}

// ListRoots returns attached roots only. Detached roots keep their rows so
// files/progress can reattach later, but they are not watched or shown as
// manageable roots until UpsertRoot reattaches the same path.
func (s *Store) ListRoots(ctx context.Context) ([]Root, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, path, online, attached, created_at
		FROM library_roots
		WHERE attached = 1
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roots []Root
	for rows.Next() {
		r, err := s.scanRoot(rows)
		if err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return roots, rows.Err()
}

// SetRootOnline flips the online flag (mount/unmount transitions).
func (s *Store) SetRootOnline(ctx context.Context, id int64, online bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE library_roots SET online = ? WHERE id = ?`, boolToInt(online), id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// DetachRoot stops actively managing a root without deleting its row. Files
// are marked offline in the same transaction so library availability changes
// atomically with the root disappearing from active lists.
func (s *Store) DetachRoot(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE library_roots SET attached = 0, online = 0 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if err := requireRow(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_files SET status = 'offline' WHERE root_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CountFilesForRoot(ctx context.Context, rootID int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_files WHERE root_id = ?`, rootID).Scan(&count)
	return count, err
}

func (s *Store) CountActiveUploadsForRoot(ctx context.Context, rootID int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM uploads WHERE root_id = ? AND status = 'active'`, rootID).Scan(&count)
	return count, err
}

// NOTE: there is deliberately no DeleteRoot. media_files.root_id references
// library_roots with no ON DELETE clause, so a root row that ever had files
// cannot be deleted — and M5's detach semantics require exactly that: the
// catalog survives a detach and re-attaches when the same path is re-added.
// Detach is an explicit state on the row, not a DELETE.

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanRoot(row rowScanner) (Root, error) {
	var r Root
	var online, attached int
	err := row.Scan(&r.ID, &r.Name, &r.Path, &online, &attached, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Root{}, ErrNotFound
	}
	if err != nil {
		return Root{}, err
	}
	r.Online = online != 0
	r.Attached = attached != 0
	return r, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func requireRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
