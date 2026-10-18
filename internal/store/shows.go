package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Show groups episode items. Metadata comes from TVmaze when matched.
type Show struct {
	ID           int64
	Title        string
	Year         *int
	Summary      *string
	TVmazeID     *int64 // nil = no TVmaze match
	SeasonOffset int    // TVmaze season = file season + offset
	NeedsCheck   bool
	TitleLocked  bool
	PosterAt     *string // set when the poster file is cached
	BackdropAt   *string // set when the TVmaze background is cached
	RefreshedAt  *string // last successful TVmaze fetch
	CreatedAt    string
	UpdatedAt    string
}

type NewShow struct {
	Title        string
	Year         *int
	Summary      *string
	TVmazeID     *int64
	SeasonOffset int
	NeedsCheck   bool
}

const (
	AliasAuto  = "auto"
	AliasOwner = "owner"
)

// ShowAlias maps a parsed show key to the show its episodes join.
type ShowAlias struct {
	Key    string
	ShowID int64
	Source string // AliasAuto|AliasOwner
}

// ShowEpisode is one cached TVmaze episode, used for titles and gaps.
type ShowEpisode struct {
	Season     int
	Number     int
	Title      string
	Airdate    *string // YYYY-MM-DD
	RuntimeMin *int
}

const showCols = `id, title, year, summary, tvmaze_id, season_offset, needs_check, title_locked,
	poster_at, backdrop_at, refreshed_at, created_at, updated_at`

func (s *Store) CreateShow(ctx context.Context, in NewShow) (Show, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Show{}, fmt.Errorf("show title is required: %w", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Show{}, err
	}
	defer tx.Rollback()
	if in.TVmazeID != nil {
		if err := requireFreeTVmazeID(ctx, tx, *in.TVmazeID, 0); err != nil {
			return Show{}, err
		}
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO shows (title, year, summary, tvmaze_id, season_offset, needs_check)
		VALUES (?, ?, ?, ?, ?, ?)`,
		in.Title, in.Year, in.Summary, in.TVmazeID, in.SeasonOffset, boolToInt(in.NeedsCheck))
	if err != nil {
		return Show{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Show{}, err
	}
	if err := tx.Commit(); err != nil {
		return Show{}, err
	}
	return s.GetShow(ctx, id)
}

func (s *Store) GetShow(ctx context.Context, id int64) (Show, error) {
	return getShow(ctx, s.db, id)
}

func (s *Store) GetShowByTVmazeID(ctx context.Context, tvmazeID int64) (Show, error) {
	return scanShow(s.db.QueryRowContext(ctx, `SELECT `+showCols+` FROM shows WHERE tvmaze_id = ?`, tvmazeID))
}

// ListShows returns every show, live episodes or not, by title.
func (s *Store) ListShows(ctx context.Context) ([]Show, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+showCols+` FROM shows ORDER BY title COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shows := []Show{}
	for rows.Next() {
		show, err := scanShow(rows)
		if err != nil {
			return nil, err
		}
		shows = append(shows, show)
	}
	return shows, rows.Err()
}

// UpdateShowParams applies partial edits: nil or unset fields are left
// unchanged. Nothing here sets TitleLocked implicitly; the caller decides.
type UpdateShowParams struct {
	Title        *string
	TitleLocked  *bool
	Year         Nullable[int]
	Summary      Nullable[string]
	TVmazeID     Nullable[int64]
	SeasonOffset *int
	NeedsCheck   *bool
	PosterAt     Nullable[string]
	BackdropAt   Nullable[string]
	RefreshedAt  Nullable[string]
}

func (s *Store) UpdateShow(ctx context.Context, id int64, p UpdateShowParams) (Show, error) {
	sets := []string{"updated_at = datetime('now')"}
	args := []any{}
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if p.Title != nil {
		title := strings.TrimSpace(*p.Title)
		if title == "" {
			return Show{}, fmt.Errorf("show title is required: %w", ErrInvalidInput)
		}
		set("title", title)
	}
	if p.TitleLocked != nil {
		set("title_locked", boolToInt(*p.TitleLocked))
	}
	if p.Year.Set {
		set("year", nullableArg(p.Year))
	}
	if p.Summary.Set {
		set("summary", nullableArg(p.Summary))
	}
	if p.TVmazeID.Set {
		set("tvmaze_id", nullableArg(p.TVmazeID))
	}
	if p.SeasonOffset != nil {
		set("season_offset", *p.SeasonOffset)
	}
	if p.NeedsCheck != nil {
		set("needs_check", boolToInt(*p.NeedsCheck))
	}
	if p.PosterAt.Set {
		set("poster_at", nullableArg(p.PosterAt))
	}
	if p.BackdropAt.Set {
		set("backdrop_at", nullableArg(p.BackdropAt))
	}
	if p.RefreshedAt.Set {
		set("refreshed_at", nullableArg(p.RefreshedAt))
	}
	args = append(args, id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Show{}, err
	}
	defer tx.Rollback()
	if p.TVmazeID.Value != nil {
		if err := requireFreeTVmazeID(ctx, tx, *p.TVmazeID.Value, id); err != nil {
			return Show{}, err
		}
	}
	res, err := tx.ExecContext(ctx, "UPDATE shows SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return Show{}, err
	}
	if err := requireRow(res); err != nil {
		return Show{}, err
	}
	if err := tx.Commit(); err != nil {
		return Show{}, err
	}
	return s.GetShow(ctx, id)
}

// DeleteShow removes a show and its aliases, returning it so the caller can
// delete its cached images. It refuses with ErrInvalidInput while any
// classified episode still belongs to it.
func (s *Store) DeleteShow(ctx context.Context, id int64) (Show, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Show{}, err
	}
	defer tx.Rollback()
	show, err := getShow(ctx, tx, id)
	if err != nil {
		return Show{}, err
	}
	members, err := showItemIDs(ctx, tx, id)
	if err != nil {
		return Show{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shows WHERE id = ?`, id); err != nil {
		return Show{}, err
	}
	if err := checkEpisodeInvariant(ctx, tx, members...); err != nil {
		return Show{}, err
	}
	return show, tx.Commit()
}

// MergeShow moves every item (live or trashed) of from into into, repoints
// from's aliases and collection memberships, then deletes from. The removed
// show is returned so the caller can delete its cached images.
func (s *Store) MergeShow(ctx context.Context, from, into int64) (Show, error) {
	if from == into {
		return Show{}, fmt.Errorf("cannot merge a show into itself: %w", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Show{}, err
	}
	defer tx.Rollback()
	removed, err := getShow(ctx, tx, from)
	if err != nil {
		return Show{}, err
	}
	if _, err := getShow(ctx, tx, into); err != nil {
		return Show{}, err
	}
	moved, err := showItemIDs(ctx, tx, from)
	if err != nil {
		return Show{}, err
	}
	steps := []struct {
		query string
		args  []any
	}{
		{`UPDATE media_items SET show_id = ?, updated_at = datetime('now') WHERE show_id = ?`, []any{into, from}},
		{`UPDATE show_aliases SET show_id = ? WHERE show_id = ?`, []any{into, from}},
		// A collection holding both keeps into's row; from's goes with from.
		{`UPDATE OR IGNORE collection_items SET show_id = ? WHERE show_id = ?`, []any{into, from}},
		{`DELETE FROM shows WHERE id = ?`, []any{from}},
		{`UPDATE shows SET updated_at = datetime('now') WHERE id = ?`, []any{into}},
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
			return Show{}, err
		}
	}
	if err := checkEpisodeInvariant(ctx, tx, moved...); err != nil {
		return Show{}, err
	}
	return removed, tx.Commit()
}

// DeleteShowIfEmpty deletes a show nothing belongs to any more, live or
// trashed, and returns it so the caller can delete its images; it returns
// nil when the show still has items or no longer exists.
func (s *Store) DeleteShowIfEmpty(ctx context.Context, id int64) (*Show, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	show, err := getShow(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_items WHERE show_id = ?`, id).Scan(&remaining); err != nil {
		return nil, err
	}
	if remaining > 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shows WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return &show, tx.Commit()
}

// ListAiringShowIDs returns matched shows whose latest episode to air on or
// before today aired on or after since (both YYYY-MM-DD).
func (s *Store) ListAiringShowIDs(ctx context.Context, since, today string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id FROM shows s
		JOIN show_episodes e ON e.show_id = s.id
		WHERE s.tvmaze_id IS NOT NULL AND e.airdate IS NOT NULL AND e.airdate <= ?
		GROUP BY s.id
		HAVING MAX(e.airdate) >= ?
		ORDER BY s.id`, today, since)
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

func (s *Store) GetShowAlias(ctx context.Context, key string) (ShowAlias, error) {
	var a ShowAlias
	err := s.db.QueryRowContext(ctx,
		`SELECT key, show_id, source FROM show_aliases WHERE key = ?`, key).
		Scan(&a.Key, &a.ShowID, &a.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return ShowAlias{}, ErrNotFound
	}
	return a, err
}

// PutShowAlias points key at a show. An owner alias always wins: an auto
// write never replaces one, and the alias actually stored is returned.
func (s *Store) PutShowAlias(ctx context.Context, key string, showID int64, source string) (ShowAlias, error) {
	if key == "" {
		return ShowAlias{}, fmt.Errorf("alias key is required: %w", ErrInvalidInput)
	}
	if source != AliasAuto && source != AliasOwner {
		return ShowAlias{}, fmt.Errorf("bad alias source %q: %w", source, ErrInvalidInput)
	}
	if err := requireShow(ctx, s.db, showID); err != nil {
		return ShowAlias{}, err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO show_aliases (key, show_id, source) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET show_id = excluded.show_id, source = excluded.source
		WHERE show_aliases.source = 'auto' OR excluded.source = 'owner'`,
		key, showID, source); err != nil {
		return ShowAlias{}, err
	}
	return s.GetShowAlias(ctx, key)
}

func (s *Store) ListShowAliases(ctx context.Context, showID int64) ([]ShowAlias, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, show_id, source FROM show_aliases WHERE show_id = ? ORDER BY key`, showID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aliases := []ShowAlias{}
	for rows.Next() {
		var a ShowAlias
		if err := rows.Scan(&a.Key, &a.ShowID, &a.Source); err != nil {
			return nil, err
		}
		aliases = append(aliases, a)
	}
	return aliases, rows.Err()
}

// ReplaceShowEpisodes swaps the show's cached TVmaze episode list wholesale.
func (s *Store) ReplaceShowEpisodes(ctx context.Context, showID int64, episodes []ShowEpisode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireShow(ctx, tx, showID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM show_episodes WHERE show_id = ?`, showID); err != nil {
		return err
	}
	for _, e := range episodes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO show_episodes (show_id, season, number, title, airdate, runtime_min)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(show_id, season, number) DO UPDATE SET
				title = excluded.title, airdate = excluded.airdate, runtime_min = excluded.runtime_min`,
			showID, e.Season, e.Number, e.Title, e.Airdate, e.RuntimeMin); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListShowEpisodes returns the cached TVmaze episodes in season/number order.
func (s *Store) ListShowEpisodes(ctx context.Context, showID int64) ([]ShowEpisode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT season, number, title, airdate, runtime_min
		FROM show_episodes WHERE show_id = ?
		ORDER BY season, number`, showID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	episodes := []ShowEpisode{}
	for rows.Next() {
		var e ShowEpisode
		var airdate sql.NullString
		var runtime sql.NullInt64
		if err := rows.Scan(&e.Season, &e.Number, &e.Title, &airdate, &runtime); err != nil {
			return nil, err
		}
		e.Airdate = stringPtr(airdate)
		e.RuntimeMin = intPtr(runtime)
		episodes = append(episodes, e)
	}
	return episodes, rows.Err()
}

func getShow(ctx context.Context, q queryer, id int64) (Show, error) {
	return scanShow(q.QueryRowContext(ctx, `SELECT `+showCols+` FROM shows WHERE id = ?`, id))
}

func requireShow(ctx context.Context, q queryer, id int64) error {
	var found int64
	err := q.QueryRowContext(ctx, `SELECT id FROM shows WHERE id = ?`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("show %d does not exist: %w", id, ErrInvalidInput)
	}
	return err
}

func requireFreeTVmazeID(ctx context.Context, q queryer, tvmazeID, exceptShowID int64) error {
	var owner int64
	err := q.QueryRowContext(ctx,
		`SELECT id FROM shows WHERE tvmaze_id = ? AND id <> ?`, tvmazeID, exceptShowID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("tvmaze show %d already belongs to show %d: %w", tvmazeID, owner, ErrConflict)
}

func showItemIDs(ctx context.Context, q queryer, showID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM media_items WHERE show_id = ? ORDER BY id`, showID)
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

func scanShow(row rowScanner) (Show, error) {
	var sh Show
	var year, tvmazeID sql.NullInt64
	var summary, posterAt, backdropAt, refreshedAt sql.NullString
	var needsCheck, titleLocked int
	err := row.Scan(&sh.ID, &sh.Title, &year, &summary, &tvmazeID, &sh.SeasonOffset,
		&needsCheck, &titleLocked, &posterAt, &backdropAt, &refreshedAt, &sh.CreatedAt, &sh.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Show{}, ErrNotFound
	}
	if err != nil {
		return Show{}, err
	}
	sh.Year = intPtr(year)
	sh.Summary = stringPtr(summary)
	sh.TVmazeID = int64Ptr(tvmazeID)
	sh.NeedsCheck = needsCheck != 0
	sh.TitleLocked = titleLocked != 0
	sh.PosterAt = stringPtr(posterAt)
	sh.BackdropAt = stringPtr(backdropAt)
	sh.RefreshedAt = stringPtr(refreshedAt)
	return sh, nil
}
