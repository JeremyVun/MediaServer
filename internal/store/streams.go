package store

import (
	"context"
	"database/sql"
)

// Stream is one elementary stream inside a media file, as reported by
// ffprobe.
type Stream struct {
	ID          int64
	FileID      int64
	StreamIndex int // ffprobe index
	Kind        string
	Codec       string
	Lang        *string
	Title       *string
	Channels    *int // audio only
	IsDefault   bool
}

// ReplaceFileStreams atomically swaps a file's stream rows for the given
// set — re-probing a file must never leave stale streams behind.
func (s *Store) ReplaceFileStreams(ctx context.Context, fileID int64, streams []Stream) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM media_streams WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	for _, st := range streams {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO media_streams (file_id, stream_index, kind, codec, lang, title, channels, is_default)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fileID, st.StreamIndex, st.Kind, st.Codec, st.Lang, st.Title, st.Channels,
			boolToInt(st.IsDefault))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListFileStreams(ctx context.Context, fileID int64) ([]Stream, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, file_id, stream_index, kind, codec, lang, title, channels, is_default
		FROM media_streams WHERE file_id = ? ORDER BY stream_index`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var streams []Stream
	for rows.Next() {
		var st Stream
		var lang, title sql.NullString
		var channels sql.NullInt64
		var isDefault int
		if err := rows.Scan(&st.ID, &st.FileID, &st.StreamIndex, &st.Kind, &st.Codec,
			&lang, &title, &channels, &isDefault); err != nil {
			return nil, err
		}
		if lang.Valid {
			st.Lang = &lang.String
		}
		if title.Valid {
			st.Title = &title.String
		}
		if channels.Valid {
			c := int(channels.Int64)
			st.Channels = &c
		}
		st.IsDefault = isDefault != 0
		streams = append(streams, st)
	}
	return streams, rows.Err()
}
