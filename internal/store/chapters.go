package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

// Chapter is one ffprobe chapter; its index is its position in the file's
// chapter list.
type Chapter struct {
	StartS float64
	EndS   float64
	Title  string
}

// ReplaceFileChapters stores the file's chapters wholesale and marks it
// scanned, so the boot sweep stops re-probing it even when it has none.
func (s *Store) ReplaceFileChapters(ctx context.Context, fileID int64, chapters []Chapter) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE media_files SET chapters_scanned = 1 WHERE id = ?`, fileID)
	if err != nil {
		return err
	}
	if err := requireRow(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_chapters WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	for i, c := range chapters {
		var title any
		if c.Title != "" {
			title = c.Title
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO media_chapters (file_id, idx, start_s, end_s, title) VALUES (?, ?, ?, ?, ?)`,
			fileID, i, c.StartS, c.EndS, title); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListFileChapters(ctx context.Context, fileID int64) ([]Chapter, error) {
	return listFileChapters(ctx, s.db, fileID)
}

// ListFilesNeedingChapters returns online files whose chapters were never
// read, for the boot-time probe sweep.
func (s *Store) ListFilesNeedingChapters(ctx context.Context) ([]File, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+prefixed("mf.", fileCols)+`
		FROM media_files mf
		JOIN library_roots lr ON lr.id = mf.root_id
		WHERE mf.chapters_scanned = 0 AND mf.status = 'online' AND lr.online = 1
		ORDER BY mf.id`)
	if err != nil {
		return nil, err
	}
	return collectFiles(rows)
}

// Span is a time range within a file, in seconds.
type Span struct {
	StartS float64
	EndS   float64
}

// Markers are the intro and credits positions the player acts on.
type Markers struct {
	Intro         *Span
	CreditsStartS *float64
}

var (
	introTitle   = regexp.MustCompile(`(?i)^(intro|opening|opening credits|op)$`)
	creditsTitle = regexp.MustCompile(`(?i)^(credits|end credits|ending|outro|ed)$`)
)

// DeriveMarkers applies the design's chapter rules: an intro chapter must
// start in the first 30 % of the duration and a credits chapter in the last
// 25 %. Chapters starting at or past the duration are ignored (ruling M6).
func DeriveMarkers(chapters []Chapter, durationS float64) Markers {
	var m Markers
	if durationS <= 0 {
		return m
	}
	for _, c := range chapters {
		if c.StartS < 0 || c.StartS >= durationS {
			continue
		}
		title := strings.TrimSpace(c.Title)
		if m.Intro == nil && introTitle.MatchString(title) && c.StartS < 0.30*durationS {
			end := min(c.EndS, durationS)
			if end > c.StartS {
				m.Intro = &Span{StartS: c.StartS, EndS: end}
			}
		}
		if m.CreditsStartS == nil && creditsTitle.MatchString(title) && c.StartS >= 0.75*durationS {
			start := c.StartS
			m.CreditsStartS = &start
		}
	}
	return m
}

// itemCreditsStart is the credits marker of the item's first online file
// (ruling M5), or nil when that file has none. fallbackDurationS stands in
// for a file not yet probed.
func itemCreditsStart(ctx context.Context, q queryer, itemID int64, fallbackDurationS float64) (*float64, error) {
	var fileID int64
	var duration sql.NullFloat64
	err := q.QueryRowContext(ctx, `
		SELECT mf.id, mf.duration_s
		FROM media_files mf
		JOIN library_roots lr ON lr.id = mf.root_id
		WHERE mf.item_id = ? AND mf.status = 'online' AND lr.online = 1
		ORDER BY mf.id
		LIMIT 1`, itemID).Scan(&fileID, &duration)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	chapters, err := listFileChapters(ctx, q, fileID)
	if err != nil || len(chapters) == 0 {
		return nil, err
	}
	durationS := fallbackDurationS
	if duration.Valid && duration.Float64 > 0 {
		durationS = duration.Float64
	}
	return DeriveMarkers(chapters, durationS).CreditsStartS, nil
}

func listFileChapters(ctx context.Context, q queryer, fileID int64) ([]Chapter, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT start_s, end_s, COALESCE(title, '')
		FROM media_chapters WHERE file_id = ?
		ORDER BY idx`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chapters := []Chapter{}
	for rows.Next() {
		var c Chapter
		if err := rows.Scan(&c.StartS, &c.EndS, &c.Title); err != nil {
			return nil, err
		}
		chapters = append(chapters, c)
	}
	return chapters, rows.Err()
}

func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
