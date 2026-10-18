package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UpsertProgress records the last playback position for an item. It counts
// as completed when the player says so, at 95 %, or once within 10 s of the
// credits marker of the item's first online file (rulings M5, B7). Without the explicit
// flag an early position resets completion, so a rewatch starts over.
func (s *Store) UpsertProgress(ctx context.Context, itemID int64, positionS, durationS float64, completedExplicit bool) (Progress, error) {
	completed := completedExplicit || reachedEnd(positionS, durationS)
	if !completed {
		credits, err := itemCreditsStart(ctx, s.db, itemID, durationS)
		if err != nil {
			return Progress{}, err
		}
		completed = pastCredits(positionS, credits)
	}
	updatedAt := FormatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO watch_progress (item_id, position_s, duration_s, completed, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
			position_s = excluded.position_s,
			duration_s = excluded.duration_s,
			completed = excluded.completed,
			updated_at = excluded.updated_at`,
		itemID, positionS, durationS, boolToInt(completed), updatedAt)
	if err != nil {
		return Progress{}, err
	}
	return Progress{PositionS: positionS, DurationS: durationS, Completed: completed, UpdatedAt: updatedAt}, nil
}

// ReevaluateProgress applies the M5 completion rule to an item's stored
// progress, for use once its chapters are known. It only ever marks a row
// completed, and keeps updated_at so up-next ordering is undisturbed. It
// returns nil when the item has no progress.
func (s *Store) ReevaluateProgress(ctx context.Context, itemID int64) (progress *Progress, changed bool, err error) {
	progress, err = s.GetProgress(ctx, itemID)
	if err != nil || progress == nil || progress.Completed {
		return progress, false, err
	}
	completed := reachedEnd(progress.PositionS, progress.DurationS)
	if !completed {
		credits, err := itemCreditsStart(ctx, s.db, itemID, progress.DurationS)
		if err != nil {
			return nil, false, err
		}
		completed = pastCredits(progress.PositionS, credits)
	}
	if !completed {
		return progress, false, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE watch_progress SET completed = 1 WHERE item_id = ?`, itemID); err != nil {
		return nil, false, err
	}
	progress.Completed = true
	return progress, true, nil
}

func reachedEnd(positionS, durationS float64) bool {
	return durationS > 0 && positionS/durationS >= 0.95
}

// creditsToleranceS forgives a viewer who stopped just short of the credits
// chapter (ruling B7).
const creditsToleranceS = 10

func pastCredits(positionS float64, creditsStartS *float64) bool {
	return creditsStartS != nil && positionS >= *creditsStartS-creditsToleranceS
}

// GetProgress returns nil when the item has no stored playback progress.
func (s *Store) GetProgress(ctx context.Context, itemID int64) (*Progress, error) {
	var progress Progress
	var completed int
	err := s.db.QueryRowContext(ctx, `
		SELECT position_s, duration_s, completed, updated_at
		FROM watch_progress
		WHERE item_id = ?`, itemID).
		Scan(&progress.PositionS, &progress.DurationS, &completed, &progress.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	progress.Completed = completed != 0
	return &progress, nil
}
