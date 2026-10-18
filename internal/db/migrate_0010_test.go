package db

import (
	"path/filepath"
	"testing"
)

func TestMigration0010StartsTheGraceOfFilesAlreadyMissing(t *testing.T) {
	sqldb, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()
	if err := migrateTo(sqldb, 9); err != nil {
		t.Fatalf("migrate to 9: %v", err)
	}
	seed := []string{
		`INSERT INTO library_roots (id, name, path) VALUES (1, 'Media', '/Volumes/Media')`,
		`INSERT INTO media_items (id, title) VALUES (1, 'Gone'), (2, 'Here')`,
		`INSERT INTO media_files (id, item_id, root_id, rel_path, size, mtime, fingerprint, status) VALUES
			(1, 1, 1, 'incoming/gone.mkv', 1, '2026-01-01 00:00:00', 'f1', 'missing'),
			(2, 2, 1, 'here.mkv', 1, '2026-01-01 00:00:00', 'f2', 'online')`,
	}
	for _, q := range seed {
		if _, err := sqldb.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var recent, online int
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM media_files
		WHERE id = 1 AND missing_since >= datetime('now', '-1 minute')`).Scan(&recent); err != nil || recent != 1 {
		t.Fatalf("missing file's missing_since not started at migration (%d, %v)", recent, err)
	}
	if err := sqldb.QueryRow(`SELECT COUNT(*) FROM media_files WHERE id = 2 AND missing_since IS NULL`).Scan(&online); err != nil || online != 1 {
		t.Fatalf("online file got a missing_since (%d, %v)", online, err)
	}
}
