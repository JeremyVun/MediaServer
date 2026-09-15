-- Operator settings changed from the web UI. The config file seeds a value
-- at boot only when no row exists; once a row is written the database is the
-- source of truth, the same rule library_roots follow.
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
