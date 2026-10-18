package store

import (
	"context"
	"database/sql"
	"errors"
)

const (
	SettingHLSCacheDir = "hls_cache_dir"
	// "1" turns on Sol for unsure parses; anything else is off.
	SettingSemanticClassification = "semantic_classification"
	// Items created before this timestamp predate the shows upgrade, so
	// the backfill checks them for owner edits.
	SettingClassifyLegacyBefore = "classify_legacy_before"
)

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return value, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value)
	return err
}

// SetSettingIfAbsent stores value only when key has none, and returns the
// value actually stored.
func (s *Store) SetSettingIfAbsent(ctx context.Context, key, value string) (string, error) {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO NOTHING`, key, value); err != nil {
		return "", err
	}
	return s.GetSetting(ctx, key)
}
