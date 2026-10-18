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
// flag an early position resets completion, so a rewatch starts over. It
// also completes the show's episodes the viewer has moved on from (ruling
// B12) and returns them.
func (s *Store) UpsertProgress(ctx context.Context, itemID int64, positionS, durationS float64, completedExplicit bool) (Progress, []MovedOn, error) {
	completed := completedExplicit || reachedEnd(positionS, durationS)
	if !completed {
		credits, err := itemCreditsStart(ctx, s.db, itemID, durationS)
		if err != nil {
			return Progress{}, nil, err
		}
		completed = pastCredits(positionS, credits)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Progress{}, nil, err
	}
	defer tx.Rollback()
	updatedAt := FormatTime(time.Now())
	_, err = tx.ExecContext(ctx, `
		INSERT INTO watch_progress (item_id, position_s, duration_s, completed, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
			position_s = excluded.position_s,
			duration_s = excluded.duration_s,
			completed = excluded.completed,
			updated_at = excluded.updated_at`,
		itemID, positionS, durationS, boolToInt(completed), updatedAt)
	if err != nil {
		return Progress{}, nil, err
	}
	movedOn, err := completeMovedOn(ctx, tx, "AND e.show_id = (SELECT show_id FROM media_items WHERE id = ?)", itemID)
	if err != nil {
		return Progress{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Progress{}, nil, err
	}
	return Progress{PositionS: positionS, DurationS: durationS, Completed: completed, UpdatedAt: updatedAt}, movedOn, nil
}

// movedOnShare is how far into an episode a viewer must have got for moving
// on to a later one to mean they watched it: files without a credits
// chapter otherwise sit at "4 min left" forever (ruling B12).
const movedOnShare = 0.80

// MovedOn is an episode completed because the viewer moved on from it.
type MovedOn struct {
	ItemID, ShowID int64
}

// CompleteMovedOnEpisodes applies ruling B12 to every show's stored
// progress, for the boot sweep. Like ReevaluateProgress it keeps
// updated_at, so up-next ordering is undisturbed.
func (s *Store) CompleteMovedOnEpisodes(ctx context.Context) ([]MovedOn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	movedOn, err := completeMovedOn(ctx, tx, "")
	if err != nil {
		return nil, err
	}
	return movedOn, tx.Commit()
}

// completeMovedOn marks completed each in-progress episode at movedOnShare
// or more that a later episode of its show, in the same sequence (specials
// or main), has newer progress than. scope narrows the shows considered.
func completeMovedOn(ctx context.Context, tx *sql.Tx, scope string, args ...any) ([]MovedOn, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.item_id, e.show_id
		FROM watch_progress p
		JOIN media_items e ON e.id = p.item_id
		WHERE p.completed = 0 AND p.duration_s > 0 AND p.position_s >= ? * p.duration_s
			AND e.type = 'episode' AND e.show_id IS NOT NULL AND e.episode IS NOT NULL AND e.deleted_at IS NULL
			`+scope+`
			AND EXISTS (
				SELECT 1
				FROM media_items l
				JOIN watch_progress lp ON lp.item_id = l.id
				WHERE l.show_id = e.show_id AND l.id != e.id AND l.type = 'episode'
					AND l.episode IS NOT NULL AND l.deleted_at IS NULL
					AND lp.updated_at > p.updated_at
					AND (COALESCE(l.season, 1) = 0) = (COALESCE(e.season, 1) = 0)
					AND (COALESCE(l.season, 1) > COALESCE(e.season, 1)
						OR (COALESCE(l.season, 1) = COALESCE(e.season, 1)
							AND l.episode > MAX(e.episode, COALESCE(e.episode_end, 0)))))`,
		append([]any{movedOnShare}, args...)...)
	if err != nil {
		return nil, err
	}
	var movedOn []MovedOn
	for rows.Next() {
		var m MovedOn
		if err := rows.Scan(&m.ItemID, &m.ShowID); err != nil {
			rows.Close()
			return nil, err
		}
		movedOn = append(movedOn, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, m := range movedOn {
		if _, err := tx.ExecContext(ctx, `UPDATE watch_progress SET completed = 1 WHERE item_id = ?`, m.ItemID); err != nil {
			return nil, err
		}
	}
	return movedOn, nil
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
