// Package store is the only package that speaks SQL. One file per entity;
// handlers and services call these methods and never touch database/sql.
//
// Times are stored as SQLite datetime text ("YYYY-MM-DD HH:MM:SS", UTC) to
// match the schema's datetime('now') defaults.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a row does not exist. Callers map it to 404.
var ErrNotFound = errors.New("not found")

// ErrInvalidInput marks caller-supplied values the store rejects (bad sort
// key, bad order). Callers map it to 400; anything else is a 500.
var ErrInvalidInput = errors.New("invalid input")

// TimeLayout is SQLite's datetime('now') text format, always UTC.
const TimeLayout = "2006-01-02 15:04:05"

type Store struct {
	db *sql.DB
}

// New wraps an opened, migrated database handle.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB exposes the underlying handle for health pings only.
func (s *Store) DB() *sql.DB { return s.db }

// FormatTime renders a time in the store's SQLite text format.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// ParseTime reads a store timestamp back into a time.Time (UTC).
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}
