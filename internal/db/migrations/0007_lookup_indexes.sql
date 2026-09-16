-- Foreign-key lookups hit on every list, detail and play request.
CREATE INDEX IF NOT EXISTS idx_files_item ON media_files(item_id);
CREATE INDEX IF NOT EXISTS idx_streams_file ON media_streams(file_id);
CREATE INDEX IF NOT EXISTS idx_collection_items_item ON collection_items(item_id);
CREATE INDEX IF NOT EXISTS idx_items_deleted_created ON media_items(deleted_at, created_at);
