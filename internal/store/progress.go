package store

import "context"

// UpsertProgress records the last playback position for an item. The
// completed flag follows SPEC-API's 95% threshold.
func (s *Store) UpsertProgress(ctx context.Context, itemID int64, positionS, durationS float64) (Progress, error) {
	completed := durationS > 0 && positionS/durationS >= 0.95
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO watch_progress (item_id, position_s, duration_s, completed, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(item_id) DO UPDATE SET
			position_s = excluded.position_s,
			duration_s = excluded.duration_s,
			completed = excluded.completed,
			updated_at = datetime('now')`,
		itemID, positionS, durationS, boolToInt(completed))
	if err != nil {
		return Progress{}, err
	}
	return Progress{PositionS: positionS, DurationS: durationS, Completed: completed}, nil
}

// GetProgress returns nil when the item has no stored playback progress.
func (s *Store) GetProgress(ctx context.Context, itemID int64) (*Progress, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT position_s, duration_s, completed
		FROM watch_progress
		WHERE item_id = ?`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var progress Progress
	var completed int
	if err := rows.Scan(&progress.PositionS, &progress.DurationS, &completed); err != nil {
		return nil, err
	}
	progress.Completed = completed != 0
	return &progress, rows.Err()
}
