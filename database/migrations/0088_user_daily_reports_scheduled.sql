ALTER TABLE user_daily_reports ADD COLUMN IF NOT EXISTS is_scheduled boolean NOT NULL DEFAULT true;
