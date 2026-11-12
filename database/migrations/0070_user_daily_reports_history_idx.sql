CREATE INDEX CONCURRENTLY IF NOT EXISTS user_daily_reports_history_idx ON user_daily_reports (user_id, report_date DESC);
