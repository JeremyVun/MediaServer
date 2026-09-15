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

func TestMigration0005CollapsesFailedJobs(t *testing.T) {
	sqldb, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The jobs schema is unchanged since 0001, so seed rows and rewind the
	// version to replay 0005 over them.
	rows := []struct {
		id     int
		status string
		load   string
	}{
		{1, "failed", "a"}, {2, "failed", "a"}, {3, "done", "a"}, {4, "failed", "a"},
		{5, "failed", "b"}, {6, "failed", "b"},
		{7, "failed", "c"}, {8, "done", "c"},
	}
	for _, r := range rows {
		if _, err := sqldb.Exec(`INSERT INTO jobs (id, type, payload, status) VALUES (?, 'probe', ?, ?)`, r.id, r.load, r.status); err != nil {
			t.Fatalf("seed %d: %v", r.id, err)
		}
	}
	if _, err := sqldb.Exec(`PRAGMA user_version = 4`); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("replay: %v", err)
	}

	var ids []int
	res, err := sqldb.Query(`SELECT id FROM jobs WHERE status = 'failed' ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer res.Close()
	for res.Next() {
		var id int
		if err := res.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// a: 1,2 superseded by done 3; 4 failed after the success stays.
	// b: 5 superseded by newer failed 6. c: 7 superseded by done 8.
	if want := []int{4, 6}; len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("remaining failed ids = %v, want %v", ids, want)
	}
	var done int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status = 'done'`).Scan(&done); err != nil || done != 2 {
		t.Fatalf("done rows = %d err=%v, want 2 untouched", done, err)
	}
}
