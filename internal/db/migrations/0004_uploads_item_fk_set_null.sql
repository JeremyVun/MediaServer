-- Fix the uploads.item_id foreign key: 0003 added it with no ON DELETE clause,
-- so purging an item (HardDeleteItem) hit a FOREIGN KEY violation while a
-- completed upload row still referenced it — which lingers until the stale-
-- upload cleanup runs (default 7 days). This broke both manual purge and the
-- retention cleanup job. SQLite can't alter a column's FK in place, so rebuild
-- the table with ON DELETE SET NULL (an upload can outlive its item; clearing
-- the link is correct, deleting the upload's own bookkeeping is not).
-- Nothing references uploads, so the drop/rename is safe with foreign_keys on.

CREATE TABLE uploads_new (
  id            TEXT PRIMARY KEY,
  filename      TEXT NOT NULL,
  size          INTEGER NOT NULL,
  received      INTEGER NOT NULL DEFAULT 0,
  root_id       INTEGER NOT NULL REFERENCES library_roots(id),
  status        TEXT NOT NULL DEFAULT 'active',
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
  checksum_xxh3 TEXT,
  rel_path      TEXT,
  item_id       INTEGER REFERENCES media_items(id) ON DELETE SET NULL
);

INSERT INTO uploads_new
  (id, filename, size, received, root_id, status, created_at, updated_at, checksum_xxh3, rel_path, item_id)
  SELECT id, filename, size, received, root_id, status, created_at, updated_at, checksum_xxh3, rel_path, item_id
  FROM uploads;

DROP TABLE uploads;
ALTER TABLE uploads_new RENAME TO uploads;
