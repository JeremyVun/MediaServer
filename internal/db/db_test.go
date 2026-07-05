package db

import (
	"path/filepath"
	"testing"
)

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	sqldb, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()

	if err := Migrate(sqldb); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	v1, err := Version(sqldb)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v1 < 1 {
		t.Fatalf("schema version = %d, want >= 1", v1)
	}

	// Second run (same process) must be a no-op.
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// Simulate a restart: reopen the same file and migrate again.
	sqldb.Close()
	sqldb2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer sqldb2.Close()
	if err := Migrate(sqldb2); err != nil {
		t.Fatalf("migrate after reopen: %v", err)
	}
	v2, err := Version(sqldb2)
	if err != nil {
		t.Fatalf("version after reopen: %v", err)
	}
	if v2 != v1 {
		t.Fatalf("schema version changed across restarts: %d != %d", v2, v1)
	}
}

func TestSchemaHasCoreTables(t *testing.T) {
	sqldb, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{
		"library_roots", "media_items", "media_files", "media_streams",
		"collections", "collection_items", "watch_progress", "uploads",
		"jobs", "items_fts",
	} {
		var name string
		err := sqldb.QueryRow(
			`SELECT name FROM sqlite_master WHERE name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	sqldb, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	_, err = sqldb.Exec(`
		INSERT INTO media_files (item_id, root_id, rel_path, size, mtime, fingerprint)
		VALUES (999, 999, 'x.mp4', 1, '2026-01-01 00:00:00', 'abc')`)
	if err == nil {
		t.Fatal("insert with dangling foreign keys succeeded; PRAGMA foreign_keys is off")
	}
}
