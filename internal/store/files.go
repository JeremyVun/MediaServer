package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// File is one media file on disk backing an item. Location is always
// (root_id, rel_path) — never an absolute path — so remounted or renamed
// volumes never orphan the catalog.
type File struct {
	ID          int64
	ItemID      int64
	RootID      int64
	RelPath     string // forward-slash, relative to the root
	Size        int64
	Mtime       string // SQLite datetime text, UTC
	Fingerprint string // hex xxh3(first 64KiB + last 64KiB + size)
	Status      string // online|offline|missing|trashed
	Container   *string
	DurationS   *float64
	Bitrate     *int64
	Width       *int
	Height      *int
	ProbedAt    *string
}

type NewFile struct {
	ItemID      int64
	RootID      int64
	RelPath     string
	Size        int64
	Mtime       time.Time
	Fingerprint string
}

func (s *Store) CreateFile(ctx context.Context, in NewFile) (File, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO media_files (item_id, root_id, rel_path, size, mtime, fingerprint)
		VALUES (?, ?, ?, ?, ?, ?)`,
		in.ItemID, in.RootID, in.RelPath, in.Size, FormatTime(in.Mtime), in.Fingerprint)
	if err != nil {
		return File{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return File{}, err
	}
	return s.GetFile(ctx, id)
}

const fileCols = `id, item_id, root_id, rel_path, size, mtime, fingerprint, status,
	container, duration_s, bitrate, width, height, probed_at`

func (s *Store) GetFile(ctx context.Context, id int64) (File, error) {
	return scanFile(s.db.QueryRowContext(ctx,
		`SELECT `+fileCols+` FROM media_files WHERE id = ?`, id))
}

// GetFileByLocation looks a file up by its identity key (root, rel_path).
func (s *Store) GetFileByLocation(ctx context.Context, rootID int64, relPath string) (File, error) {
	return scanFile(s.db.QueryRowContext(ctx,
		`SELECT `+fileCols+` FROM media_files WHERE root_id = ? AND rel_path = ?`,
		rootID, relPath))
}

// GetFilesByFingerprint supports move detection: same content, new location.
func (s *Store) GetFilesByFingerprint(ctx context.Context, fingerprint string) ([]File, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+fileCols+` FROM media_files WHERE fingerprint = ? ORDER BY id`, fingerprint)
	if err != nil {
		return nil, err
	}
	return collectFiles(rows)
}

func (s *Store) ListFilesForItem(ctx context.Context, itemID int64) ([]File, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+fileCols+` FROM media_files WHERE item_id = ? ORDER BY id`, itemID)
	if err != nil {
		return nil, err
	}
	return collectFiles(rows)
}

func (s *Store) ListFilesForRoot(ctx context.Context, rootID int64) ([]File, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+fileCols+` FROM media_files WHERE root_id = ? ORDER BY id`, rootID)
	if err != nil {
		return nil, err
	}
	return collectFiles(rows)
}

// SetFileStatus updates one file's status (online|offline|missing|trashed).
func (s *Store) SetFileStatus(ctx context.Context, id int64, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE media_files SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// SetRootFilesStatus bulk-updates every file on a root — the unmount/remount
// transition. Returns the number of files touched.
func (s *Store) SetRootFilesStatus(ctx context.Context, rootID int64, status string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE media_files SET status = ? WHERE root_id = ?`, status, rootID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RelocateFile re-points a file at a new (root, rel_path) after move
// detection, refreshing its stat and bringing it back online in one atomic
// statement — a crash can never leave the row half-moved. The row (and the
// item/progress/collections hanging off it) is preserved.
func (s *Store) RelocateFile(ctx context.Context, id, rootID int64, relPath string, size int64, mtime time.Time, fingerprint string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_files
		SET root_id = ?, rel_path = ?, size = ?, mtime = ?, fingerprint = ?, status = 'online'
		WHERE id = ?`,
		rootID, relPath, size, FormatTime(mtime), fingerprint, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ProbeResult is what ffprobe learned about a file.
type ProbeResult struct {
	Container string
	DurationS float64
	Bitrate   int64
	Width     int
	Height    int
}

// UpdateFileProbe records probe output and stamps probed_at.
func (s *Store) UpdateFileProbe(ctx context.Context, id int64, p ProbeResult) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_files
		SET container = ?, duration_s = ?, bitrate = ?, width = ?, height = ?,
		    probed_at = datetime('now')
		WHERE id = ?`,
		p.Container, p.DurationS, p.Bitrate, p.Width, p.Height, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// UpdateFileStat refreshes size/mtime/fingerprint after a change on disk.
func (s *Store) UpdateFileStat(ctx context.Context, id int64, size int64, mtime time.Time, fingerprint string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE media_files SET size = ?, mtime = ?, fingerprint = ? WHERE id = ?`,
		size, FormatTime(mtime), fingerprint, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func collectFiles(rows *sql.Rows) ([]File, error) {
	defer rows.Close()
	var files []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func scanFile(row rowScanner) (File, error) {
	var f File
	var container, probedAt sql.NullString
	var duration sql.NullFloat64
	var bitrate sql.NullInt64
	var width, height sql.NullInt64
	err := row.Scan(&f.ID, &f.ItemID, &f.RootID, &f.RelPath, &f.Size, &f.Mtime,
		&f.Fingerprint, &f.Status, &container, &duration, &bitrate, &width, &height, &probedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	if container.Valid {
		f.Container = &container.String
	}
	if duration.Valid {
		f.DurationS = &duration.Float64
	}
	if bitrate.Valid {
		f.Bitrate = &bitrate.Int64
	}
	if width.Valid {
		w := int(width.Int64)
		f.Width = &w
	}
	if height.Valid {
		h := int(height.Int64)
		f.Height = &h
	}
	if probedAt.Valid {
		f.ProbedAt = &probedAt.String
	}
	return f, nil
}
