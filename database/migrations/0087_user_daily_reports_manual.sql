ALTER TABLE user_daily_reports ADD COLUMN IF NOT EXISTS manual_reports jsonb NOT NULL DEFAULT '[]'::jsonb;
