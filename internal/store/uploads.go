package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrConflict marks a state mismatch, such as a concurrent upload offset
// update. Callers map it to 409.
var ErrConflict = errors.New("conflict")

type Upload struct {
	ID           string
	Filename     string
	Size         int64
	Received     int64
	RootID       int64
	Status       string // active|complete|aborted
	CreatedAt    string
	UpdatedAt    string
	ChecksumXXH3 *string
	RelPath      *string
	ItemID       *int64
}

type NewUpload struct {
	ID           string
	Filename     string
	Size         int64
	RootID       int64
	ChecksumXXH3 *string
}

func (s *Store) CreateUpload(ctx context.Context, in NewUpload) (Upload, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO uploads (id, filename, size, root_id, checksum_xxh3)
		VALUES (?, ?, ?, ?, ?)`,
		in.ID, in.Filename, in.Size, in.RootID, in.ChecksumXXH3)
	if err != nil {
		return Upload{}, err
	}
	return s.GetUpload(ctx, in.ID)
}

func (s *Store) GetUpload(ctx context.Context, id string) (Upload, error) {
	return scanUpload(s.db.QueryRowContext(ctx, `SELECT `+uploadCols+` FROM uploads WHERE id = ?`, id))
}

func (s *Store) UpdateUploadReceived(ctx context.Context, id string, expected, received int64) (Upload, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE uploads
		SET received = ?, updated_at = datetime('now')
		WHERE id = ? AND received = ? AND status = 'active'`,
		received, id, expected)
	if err != nil {
		return Upload{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Upload{}, err
	}
	if n == 0 {
		if _, err := s.GetUpload(ctx, id); err != nil {
			return Upload{}, err
		}
		return Upload{}, ErrConflict
	}
	return s.GetUpload(ctx, id)
}

func (s *Store) MarkUploadComplete(ctx context.Context, id, relPath string) (Upload, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE uploads
		SET status = 'complete', rel_path = ?, updated_at = datetime('now')
		WHERE id = ? AND status = 'active'`,
		relPath, id)
	if err != nil {
		return Upload{}, err
	}
	if err := requireRow(res); err != nil {
		if _, getErr := s.GetUpload(ctx, id); getErr != nil {
			return Upload{}, getErr
		}
		return Upload{}, ErrConflict
	}
	return s.GetUpload(ctx, id)
}

func (s *Store) AbortUpload(ctx context.Context, id string) (Upload, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE uploads
		SET status = 'aborted', updated_at = datetime('now')
		WHERE id = ? AND status = 'active'`,
		id)
	if err != nil {
		return Upload{}, err
	}
	if err := requireRow(res); err != nil {
		if _, getErr := s.GetUpload(ctx, id); getErr != nil {
			return Upload{}, getErr
		}
		return Upload{}, ErrConflict
	}
	return s.GetUpload(ctx, id)
}

func (s *Store) ListUploadsBefore(ctx context.Context, cutoff time.Time, limit int) ([]Upload, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+uploadCols+`
		FROM uploads
		WHERE updated_at <= ?
		ORDER BY updated_at, id
		LIMIT ?`, FormatTime(cutoff), limit)
	if err != nil {
		return nil, err
	}
	return collectUploads(rows)
}

func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// AttachUploadItem records the item produced by a completed upload location.
// It returns the uploads that changed so callers can publish upload.complete
// handoff events exactly once.
func (s *Store) AttachUploadItem(ctx context.Context, rootID int64, relPath string, itemID int64) ([]Upload, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT `+uploadCols+`
		FROM uploads
		WHERE root_id = ? AND rel_path = ? AND status = 'complete' AND item_id IS NULL`,
		rootID, relPath)
	if err != nil {
		return nil, err
	}
	uploads, err := collectUploads(rows)
	if err != nil {
		return nil, err
	}
	if len(uploads) == 0 {
		return nil, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE uploads
		SET item_id = ?, updated_at = datetime('now')
		WHERE root_id = ? AND rel_path = ? AND status = 'complete' AND item_id IS NULL`,
		itemID, rootID, relPath); err != nil {
		return nil, err
	}
	for i := range uploads {
		uploads[i].ItemID = &itemID
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return uploads, nil
}

const uploadCols = `id, filename, size, received, root_id, status, created_at, updated_at,
	checksum_xxh3, rel_path, item_id`

func collectUploads(rows *sql.Rows) ([]Upload, error) {
	defer rows.Close()
	var uploads []Upload
	for rows.Next() {
		upload, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, upload)
	}
	return uploads, rows.Err()
}

func scanUpload(row rowScanner) (Upload, error) {
	var upload Upload
	var checksum, relPath sql.NullString
	var itemID sql.NullInt64
	err := row.Scan(
		&upload.ID,
		&upload.Filename,
		&upload.Size,
		&upload.Received,
		&upload.RootID,
		&upload.Status,
		&upload.CreatedAt,
		&upload.UpdatedAt,
		&checksum,
		&relPath,
		&itemID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	if checksum.Valid {
		upload.ChecksumXXH3 = &checksum.String
	}
	if relPath.Valid {
		upload.RelPath = &relPath.String
	}
	if itemID.Valid {
		id := itemID.Int64
		upload.ItemID = &id
	}
	return upload, nil
}
