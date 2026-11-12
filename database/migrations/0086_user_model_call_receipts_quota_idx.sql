CREATE INDEX CONCURRENTLY IF NOT EXISTS user_model_call_receipts_user_purpose_created_idx ON user_model_call_receipts (user_id, purpose, created_at DESC);
