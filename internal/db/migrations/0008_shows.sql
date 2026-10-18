-- Shows: episodes group under a show with season/episode numbers, TVmaze
-- metadata is cached, chapters feed intro/credits markers, and collections
-- can hold whole shows.

CREATE TABLE shows (
  id            INTEGER PRIMARY KEY,
  title         TEXT NOT NULL,
  year          INTEGER,
  summary       TEXT,
  network       TEXT,
  tvmaze_id     INTEGER UNIQUE,
  season_offset INTEGER NOT NULL DEFAULT 0,
  needs_check   INTEGER NOT NULL DEFAULT 1,
  title_locked  INTEGER NOT NULL DEFAULT 0,
  poster_at     TEXT,
  backdrop_at   TEXT,
  refreshed_at  TEXT,
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE show_aliases (
  key      TEXT PRIMARY KEY,
  show_id  INTEGER NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
  source   TEXT NOT NULL
);
CREATE INDEX idx_show_aliases_show ON show_aliases(show_id);

CREATE TABLE show_episodes (
  show_id     INTEGER NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
  season      INTEGER NOT NULL,
  number      INTEGER NOT NULL,
  title       TEXT NOT NULL,
  airdate     TEXT,
  runtime_min INTEGER,
  PRIMARY KEY (show_id, season, number)
);

ALTER TABLE media_items ADD COLUMN show_id     INTEGER REFERENCES shows(id) ON DELETE SET NULL;
ALTER TABLE media_items ADD COLUMN season      INTEGER;
ALTER TABLE media_items ADD COLUMN episode     INTEGER;
ALTER TABLE media_items ADD COLUMN episode_end INTEGER;
ALTER TABLE media_items ADD COLUMN show_key    TEXT;
ALTER TABLE media_items ADD COLUMN needs_check INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media_items ADD COLUMN locked      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE media_items ADD COLUMN classified_at TEXT;
CREATE INDEX idx_items_show ON media_items(show_id);

CREATE TABLE media_chapters (
  file_id  INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
  idx      INTEGER NOT NULL,
  start_s  REAL NOT NULL,
  end_s    REAL NOT NULL,
  title    TEXT,
  PRIMARY KEY (file_id, idx)
);
ALTER TABLE media_files ADD COLUMN chapters_scanned INTEGER NOT NULL DEFAULT 0;

-- SQLite can't relax NOT NULL or add a CHECK in place, so rebuild as 0004
-- did. Nothing references collection_items, so the drop is FK-safe.
CREATE TABLE collection_items_new (
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  item_id       INTEGER REFERENCES media_items(id) ON DELETE CASCADE,
  show_id       INTEGER REFERENCES shows(id) ON DELETE CASCADE,
  sort_order    INTEGER NOT NULL,
  CHECK ((item_id IS NULL) <> (show_id IS NULL))
);

INSERT INTO collection_items_new (collection_id, item_id, show_id, sort_order)
  SELECT collection_id, item_id, NULL, sort_order FROM collection_items;

DROP TABLE collection_items;
ALTER TABLE collection_items_new RENAME TO collection_items;

CREATE UNIQUE INDEX uq_collection_items_item ON collection_items(collection_id, item_id) WHERE item_id IS NOT NULL;
CREATE UNIQUE INDEX uq_collection_items_show ON collection_items(collection_id, show_id) WHERE show_id IS NOT NULL;
CREATE INDEX idx_collection_items_item ON collection_items(item_id) WHERE item_id IS NOT NULL;
CREATE INDEX idx_collection_items_show ON collection_items(show_id) WHERE show_id IS NOT NULL;

CREATE VIRTUAL TABLE shows_fts USING fts5(
  title, content='shows', content_rowid='id', tokenize='unicode61'
);
CREATE TRIGGER shows_ai AFTER INSERT ON shows BEGIN
  INSERT INTO shows_fts(rowid, title) VALUES (new.id, new.title);
END;
CREATE TRIGGER shows_au AFTER UPDATE OF title ON shows BEGIN
  INSERT INTO shows_fts(shows_fts, rowid, title) VALUES ('delete', old.id, old.title);
  INSERT INTO shows_fts(rowid, title) VALUES (new.id, new.title);
END;
CREATE TRIGGER shows_ad AFTER DELETE ON shows BEGIN
  INSERT INTO shows_fts(shows_fts, rowid, title) VALUES ('delete', old.id, old.title);
END;
