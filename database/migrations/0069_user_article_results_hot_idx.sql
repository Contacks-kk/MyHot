CREATE INDEX CONCURRENTLY IF NOT EXISTS user_article_results_hot_idx ON user_article_results (user_id, created_at DESC, relevance_score DESC);
