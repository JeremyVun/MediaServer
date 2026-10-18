-- TMDB movie metadata: the matched movie, when its backdrop was cached, and
-- when matching last settled. summary_locked marks a summary the owner
-- wrote, which TMDB never replaces; until now only the owner wrote them.

ALTER TABLE media_items ADD COLUMN tmdb_id         INTEGER;
ALTER TABLE media_items ADD COLUMN art_at          TEXT;
ALTER TABLE media_items ADD COLUMN tmdb_checked_at TEXT;
ALTER TABLE media_items ADD COLUMN summary_locked  INTEGER NOT NULL DEFAULT 0;

UPDATE media_items SET summary_locked = 1 WHERE summary IS NOT NULL AND trim(summary) <> '';
