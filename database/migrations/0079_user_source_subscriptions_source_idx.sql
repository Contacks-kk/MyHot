CREATE INDEX CONCURRENTLY IF NOT EXISTS user_source_subscriptions_source_active_idx ON user_source_subscriptions (source_id, interval_minutes, reuse_window_minutes) WHERE enabled;
