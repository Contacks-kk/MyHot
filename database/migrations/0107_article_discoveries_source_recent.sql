CREATE INDEX CONCURRENTLY IF NOT EXISTS article_discoveries_source_recent_idx ON article_discoveries(source_id,discovered_at DESC,article_id);
