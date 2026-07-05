-- Full v1 schema per docs/SPEC-BACKEND.md.

CREATE TABLE library_roots (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL,
  path         TEXT NOT NULL UNIQUE,          -- absolute path of the root
  online       INTEGER NOT NULL DEFAULT 1,
  created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE media_items (
  id           INTEGER PRIMARY KEY,
  type         TEXT NOT NULL DEFAULT 'video', -- video|movie|episode (v1 uses 'video' + parser hints)
  title        TEXT NOT NULL,
  year         INTEGER,
  summary      TEXT,
  created_at   TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
  deleted_at   TEXT                            -- soft delete; set when trashed
);

CREATE TABLE media_files (
  id           INTEGER PRIMARY KEY,
  item_id      INTEGER NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
  root_id      INTEGER NOT NULL REFERENCES library_roots(id),
  rel_path     TEXT NOT NULL,                 -- forward-slash path relative to root
  size         INTEGER NOT NULL,
  mtime        TEXT NOT NULL,
  fingerprint  TEXT NOT NULL,                 -- hex xxh3(first 64KiB + last 64KiB + size)
  status       TEXT NOT NULL DEFAULT 'online',-- online|offline|missing|trashed
  container    TEXT,                          -- from probe: mkv|mp4|...
  duration_s   REAL,
  bitrate      INTEGER,
  width        INTEGER,
  height       INTEGER,
  probed_at    TEXT,
  UNIQUE(root_id, rel_path)
);
CREATE INDEX idx_files_fingerprint ON media_files(fingerprint);

CREATE TABLE media_streams (
  id           INTEGER PRIMARY KEY,
  file_id      INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
  stream_index INTEGER NOT NULL,              -- ffprobe index
  kind         TEXT NOT NULL,                 -- video|audio|subtitle
  codec        TEXT NOT NULL,                 -- h264|hevc|aac|ac3|dts|subrip|hdmv_pgs_subtitle|...
  lang         TEXT,
  title        TEXT,
  channels     INTEGER,                       -- audio only
  is_default   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE collections (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL,
  created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE collection_items (
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  item_id       INTEGER NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
  sort_order    INTEGER NOT NULL,
  PRIMARY KEY (collection_id, item_id)
);

CREATE TABLE watch_progress (
  item_id      INTEGER PRIMARY KEY REFERENCES media_items(id) ON DELETE CASCADE,
  position_s   REAL NOT NULL,
  duration_s   REAL NOT NULL,
  completed    INTEGER NOT NULL DEFAULT 0,    -- position/duration >= 0.95
  updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE uploads (
  id           TEXT PRIMARY KEY,              -- uuid
  filename     TEXT NOT NULL,
  size         INTEGER NOT NULL,
  received     INTEGER NOT NULL DEFAULT 0,    -- contiguous bytes persisted
  root_id      INTEGER NOT NULL REFERENCES library_roots(id),
  status       TEXT NOT NULL DEFAULT 'active',-- active|complete|aborted
  created_at   TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE jobs (
  id           INTEGER PRIMARY KEY,
  type         TEXT NOT NULL,                 -- probe|thumbnail|cleanup|reconcile
  payload      TEXT NOT NULL,                 -- JSON
  status       TEXT NOT NULL DEFAULT 'queued',-- queued|running|done|failed
  attempts     INTEGER NOT NULL DEFAULT 0,
  run_at       TEXT NOT NULL DEFAULT (datetime('now')),
  started_at   TEXT,
  finished_at  TEXT,
  error        TEXT
);
CREATE INDEX idx_jobs_pending ON jobs(status, run_at);

-- Full-text search, kept in sync by triggers on media_items
CREATE VIRTUAL TABLE items_fts USING fts5(
  title, content='media_items', content_rowid='id', tokenize='unicode61'
);
CREATE TRIGGER items_ai AFTER INSERT ON media_items BEGIN
  INSERT INTO items_fts(rowid, title) VALUES (new.id, new.title);
END;
CREATE TRIGGER items_au AFTER UPDATE OF title ON media_items BEGIN
  INSERT INTO items_fts(items_fts, rowid, title) VALUES ('delete', old.id, old.title);
  INSERT INTO items_fts(rowid, title) VALUES (new.id, new.title);
END;
CREATE TRIGGER items_ad AFTER DELETE ON media_items BEGIN
  INSERT INTO items_fts(items_fts, rowid, title) VALUES ('delete', old.id, old.title);
END;
