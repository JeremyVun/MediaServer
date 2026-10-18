package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var preservedQueries = map[string]string{
	"items":            `SELECT id, type, title, year, summary, created_at, updated_at, deleted_at FROM media_items ORDER BY id`,
	"files":            `SELECT id, item_id, root_id, rel_path, size, status, duration_s FROM media_files ORDER BY id`,
	"collections":      `SELECT id, name, created_at FROM collections ORDER BY id`,
	"collection_items": `SELECT collection_id, item_id, sort_order FROM collection_items ORDER BY collection_id, sort_order, item_id`,
	"watch_progress":   `SELECT item_id, position_s, duration_s, completed, updated_at FROM watch_progress ORDER BY item_id`,
	"items_fts":        `SELECT rowid FROM items_fts WHERE items_fts MATCH 'a* OR e* OR s*' ORDER BY rowid`,
}

func dumpRows(t *testing.T, sqldb *sql.DB, query string) []string {
	t.Helper()
	rows, err := sqldb.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			parts[i] = fmt.Sprint(v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func snapshot(t *testing.T, sqldb *sql.DB) map[string][]string {
	t.Helper()
	snap := map[string][]string{}
	for name, q := range preservedQueries {
		snap[name] = dumpRows(t, sqldb, q)
	}
	return snap
}

func assertPreservedAndPending(t *testing.T, sqldb *sql.DB, before map[string][]string) {
	t.Helper()
	after := snapshot(t, sqldb)
	for name := range preservedQueries {
		if !reflect.DeepEqual(before[name], after[name]) {
			t.Errorf("%s changed across 0008: %d rows before, %d after", name, len(before[name]), len(after[name]))
		}
	}
	var classified int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM media_items WHERE classified_at IS NOT NULL OR show_id IS NOT NULL`).Scan(&classified); err != nil {
		t.Fatal(err)
	}
	if classified != 0 {
		t.Errorf("%d items classified or attached by the migration, want 0", classified)
	}
	var check string
	if err := sqldb.QueryRow(`PRAGMA foreign_key_check`).Scan(&check); err != sql.ErrNoRows {
		t.Errorf("foreign_key_check reports violations (%v)", err)
	}
}

func TestMigration0008PreservesCollectionsAndAddsShows(t *testing.T) {
	sqldb, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()
	if err := migrateTo(sqldb, 7); err != nil {
		t.Fatalf("migrate to 7: %v", err)
	}
	seed := []string{
		`INSERT INTO library_roots (id, name, path) VALUES (1, 'Media', '/Volumes/Media')`,
		`INSERT INTO media_items (id, type, title) VALUES (1, 'episode', 'Euphoria S01E01'), (2, 'video', 'Alpha'), (3, 'episode', 'Silo S01E02')`,
		`INSERT INTO media_files (id, item_id, root_id, rel_path, size, mtime, fingerprint) VALUES
			(1, 1, 1, 'a.mkv', 1, '2026-01-01 00:00:00', 'f1'), (2, 2, 1, 'b.mkv', 1, '2026-01-01 00:00:00', 'f2')`,
		`INSERT INTO collections (id, name) VALUES (1, 'euphoria'), (2, 'to watch')`,
		`INSERT INTO collection_items (collection_id, item_id, sort_order) VALUES (1, 1, 1), (2, 3, 1), (2, 1, 2), (2, 2, 7)`,
		`INSERT INTO watch_progress (item_id, position_s, duration_s, completed) VALUES (1, 30, 100, 0)`,
	}
	for _, q := range seed {
		if _, err := sqldb.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	before := snapshot(t, sqldb)
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	assertPreservedAndPending(t, sqldb, before)

	if _, err := sqldb.Exec(`INSERT INTO shows (id, title) VALUES (1, 'Euphoria')`); err != nil {
		t.Fatalf("insert show: %v", err)
	}
	var needsCheck int
	if err := sqldb.QueryRow(`SELECT needs_check FROM shows WHERE id = 1`).Scan(&needsCheck); err != nil || needsCheck != 1 {
		t.Errorf("new show needs_check = %d (%v), want default 1", needsCheck, err)
	}
	if _, err := sqldb.Exec(`INSERT INTO collection_items (collection_id, show_id, sort_order) VALUES (2, 1, 8)`); err != nil {
		t.Errorf("show membership rejected: %v", err)
	}
	rejected := map[string]string{
		"neither":          `INSERT INTO collection_items (collection_id, sort_order) VALUES (1, 9)`,
		"both":             `INSERT INTO collection_items (collection_id, item_id, show_id, sort_order) VALUES (1, 2, 1, 9)`,
		"duplicate item":   `INSERT INTO collection_items (collection_id, item_id, sort_order) VALUES (1, 1, 9)`,
		"duplicate show":   `INSERT INTO collection_items (collection_id, show_id, sort_order) VALUES (2, 1, 9)`,
		"dangling show":    `INSERT INTO collection_items (collection_id, show_id, sort_order) VALUES (1, 99, 9)`,
		"dangling chapter": `INSERT INTO media_chapters (file_id, idx, start_s, end_s) VALUES (99, 0, 0, 1)`,
	}
	for name, q := range rejected {
		if _, err := sqldb.Exec(q); err == nil {
			t.Errorf("%s: insert succeeded, want constraint failure", name)
		}
	}

	var hit int64
	if err := sqldb.QueryRow(`SELECT rowid FROM shows_fts WHERE shows_fts MATCH 'euph*'`).Scan(&hit); err != nil || hit != 1 {
		t.Errorf("shows_fts insert trigger: rowid %d, err %v", hit, err)
	}
	if _, err := sqldb.Exec(`UPDATE shows SET title = 'Silo' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := sqldb.QueryRow(`SELECT rowid FROM shows_fts WHERE shows_fts MATCH 'euph*'`).Scan(&hit); err != sql.ErrNoRows {
		t.Errorf("shows_fts kept the old title after rename (err %v)", err)
	}
	if _, err := sqldb.Exec(`DELETE FROM shows WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := sqldb.QueryRow(`SELECT rowid FROM shows_fts WHERE shows_fts MATCH 'silo*'`).Scan(&hit); err != sql.ErrNoRows {
		t.Errorf("shows_fts kept a deleted show (err %v)", err)
	}
	var members int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM collection_items WHERE show_id IS NOT NULL`).Scan(&members); err != nil || members != 0 {
		t.Errorf("show membership survived show delete: %d (%v)", members, err)
	}
}

// Runs only on the mini, against a read-only copy of the live catalog.
func TestMigration0008OnProductionCopy(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	prod := filepath.Join(home, "media-server", "data", "media.db")
	if _, err := os.Stat(prod); err != nil {
		t.Skip("production database not present")
	}
	copyPath := filepath.Join(t.TempDir(), "media.db")
	src, err := sql.Open("sqlite", "file:"+prod+"?mode=ro")
	if err != nil {
		t.Fatalf("open production read-only: %v", err)
	}
	_, err = src.Exec(`VACUUM INTO ?`, copyPath)
	src.Close()
	if err != nil {
		t.Fatalf("copy production database: %v", err)
	}

	sqldb, err := Open(copyPath)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer sqldb.Close()
	version, err := Version(sqldb)
	if err != nil {
		t.Fatal(err)
	}
	if version >= 8 {
		t.Skipf("production database is already at version %d", version)
	}
	before := snapshot(t, sqldb)
	if len(before["items"]) == 0 {
		t.Fatal("production copy has no items")
	}
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate production copy: %v", err)
	}
	assertPreservedAndPending(t, sqldb, before)
	t.Logf("preserved %d items, %d files, %d collections, %d collection rows",
		len(before["items"]), len(before["files"]), len(before["collections"]), len(before["collection_items"]))
}
