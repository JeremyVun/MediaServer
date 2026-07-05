ALTER TABLE uploads ADD COLUMN checksum_xxh3 TEXT;
ALTER TABLE uploads ADD COLUMN rel_path TEXT;
ALTER TABLE uploads ADD COLUMN item_id INTEGER REFERENCES media_items(id);
