// Package db opens the SQLite database and applies embedded migrations.
//
// Migrations are numbered .sql files in migrations/ applied in order inside
// a transaction each; progress is tracked with PRAGMA user_version, so
// re-running Migrate on an up-to-date database is a no-op.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if needed) the SQLite database at path with WAL mode,
// foreign keys, and a 5 s busy timeout.
//
// _txlock=immediate makes every transaction begin with BEGIN IMMEDIATE so it
// takes the write lock up front, where busy_timeout applies. Without it,
// database/sql's default deferred transactions upgrade reader→writer mid-flight
// and hit an un-retryable SQLITE_BUSY under concurrent writers (e.g. a
// bulk-copy storm feeding several job workers at once). Readers still run
// concurrently under WAL.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + url.PathEscape(path) +
		"?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(wal)" +
		"&_pragma=synchronous(normal)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return sqldb, nil
}

// Migrate applies all pending migrations. Safe to call on every startup.
func Migrate(sqldb *sql.DB) error {
	var current int
	if err := sqldb.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := sqldb.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(raw)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		// PRAGMA does not accept bind parameters; version comes from the
		// validated filename, not user input.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set user_version %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		current = version
	}
	return nil
}

// Version reports the current schema version (PRAGMA user_version).
func Version(sqldb *sql.DB) (int, error) {
	var v int
	err := sqldb.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

func migrationNames() ([]string, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %s: name must be NNNN_description.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("migration %s: bad version prefix", name)
	}
	return v, nil
}
