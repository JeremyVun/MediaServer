-- When each file went missing, for build ruling B16: an item whose files
-- stay missing past a grace period moves to Trash. The trigger keeps it in
-- step with every status write, so no code path can forget it. Files already
-- missing start their grace now. auto_trashed marks an item the server
-- trashed that way, which restores itself when a file returns (B19); an item
-- the owner trashed never does.

ALTER TABLE media_files ADD COLUMN missing_since TEXT;
ALTER TABLE media_items ADD COLUMN auto_trashed INTEGER NOT NULL DEFAULT 0;

UPDATE media_files SET missing_since = datetime('now') WHERE status = 'missing';

CREATE TRIGGER files_missing_since AFTER UPDATE OF status ON media_files
WHEN new.status IS NOT old.status
BEGIN
  UPDATE media_files
  SET missing_since = CASE WHEN new.status = 'missing' THEN datetime('now') END
  WHERE id = new.id;
END;
