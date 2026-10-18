package db

import (
	"path/filepath"
	"reflect"
	"testing"
)

var showsQueries = map[string]string{
	"shows":            `SELECT id, title, tvmaze_id, season_offset, needs_check, title_locked FROM shows ORDER BY id`,
	"aliases":          `SELECT key, show_id, source FROM show_aliases ORDER BY key`,
	"show_episodes":    `SELECT show_id, season, number, title FROM show_episodes ORDER BY show_id, season, number`,
	"items":            `SELECT id, type, show_id, season, episode, classified_at, locked, summary, summary_locked FROM media_items ORDER BY id`,
	"collection_items": `SELECT collection_id, item_id, show_id, sort_order FROM collection_items ORDER BY collection_id, sort_order`,
	"chapters":         `SELECT file_id, idx, start_s, title FROM media_chapters ORDER BY file_id, idx`,
	"files":            `SELECT id, chapters_scanned FROM media_files ORDER BY id`,
	"shows_fts":        `SELECT rowid FROM shows_fts WHERE shows_fts MATCH 'eu*' ORDER BY rowid`,
}

// probe: 0008/0009 run once; reboots (reopen + Migrate) keep every shows row, the rebuilt collection order and summary_locked as they were.
func TestProbe_Migration_RebootsKeepShowsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	sqldb, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(sqldb, 7); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO library_roots (id, name, path) VALUES (1, 'Media', '/Volumes/Media')`,
		`INSERT INTO media_items (id, type, title, summary) VALUES
			(1, 'episode', 'E1', NULL), (2, 'movie', 'Owned', 'mine'), (3, 'movie', 'Blank', '   '), (4, 'video', 'Clip', '')`,
		`INSERT INTO media_files (id, item_id, root_id, rel_path, size, mtime, fingerprint) VALUES
			(1, 1, 1, 'e1.mkv', 1, '2026-01-01 00:00:00', 'f1')`,
		`INSERT INTO collections (id, name) VALUES (1, 'a'), (2, 'b')`,
		`INSERT INTO collection_items (collection_id, item_id, sort_order) VALUES (1, 2, 5), (1, 1, 2), (2, 1, 9), (2, 3, 1), (2, 4, 4)`,
	} {
		if _, err := sqldb.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(sqldb); err != nil {
		t.Fatal(err)
	}
	var locked []int
	rows, err := sqldb.Query(`SELECT summary_locked FROM media_items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		locked = append(locked, v)
	}
	rows.Close()
	if !reflect.DeepEqual(locked, []int{0, 1, 0, 0}) {
		t.Errorf("summary_locked = %v, want only the non-blank owner summary locked", locked)
	}
	if got := dumpRows(t, sqldb, `SELECT collection_id, item_id, sort_order FROM collection_items ORDER BY collection_id, sort_order`); !reflect.DeepEqual(got,
		[]string{"1|1|2", "1|2|5", "2|3|1", "2|4|4", "2|1|9"}) {
		t.Errorf("rebuilt collection_items = %v, want every row and sort_order kept", got)
	}
	for _, q := range []string{
		`INSERT INTO shows (id, title, tvmaze_id, needs_check) VALUES (1, 'Euphoria', 41, 0)`,
		`INSERT INTO show_aliases (key, show_id, source) VALUES ('euphoria us', 1, 'auto'), ('euphoria', 1, 'owner')`,
		`INSERT INTO show_episodes (show_id, season, number, title) VALUES (1, 1, 1, 'Pilot')`,
		`UPDATE media_items SET show_id = 1, season = 1, episode = 1, classified_at = '2026-01-02 00:00:00', locked = 1 WHERE id = 1`,
		`UPDATE media_items SET summary_locked = 0 WHERE id = 2`,
		`INSERT INTO collection_items (collection_id, show_id, sort_order) VALUES (1, 1, 7)`,
		`INSERT INTO media_chapters (file_id, idx, start_s, end_s, title) VALUES (1, 0, 0, 90, 'Intro')`,
		`UPDATE media_files SET chapters_scanned = 1 WHERE id = 1`,
	} {
		if _, err := sqldb.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	before := map[string][]string{}
	for name, q := range showsQueries {
		before[name] = dumpRows(t, sqldb, q)
	}
	sqldb.Close()

	for boot := 1; boot <= 3; boot++ {
		sqldb, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := Migrate(sqldb); err != nil {
			t.Fatalf("boot %d: %v", boot, err)
		}
		names, _ := migrationNames()
		want, _ := migrationVersion(names[len(names)-1])
		if v, err := Version(sqldb); err != nil || v != want {
			t.Fatalf("boot %d: version %d (%v), want %d", boot, v, err, want)
		}
		for name, q := range showsQueries {
			if got := dumpRows(t, sqldb, q); !reflect.DeepEqual(got, before[name]) {
				t.Errorf("boot %d: %s = %v, want %v", boot, name, got, before[name])
			}
		}
		sqldb.Close()
	}
}
