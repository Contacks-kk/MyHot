ALTER TABLE user_source_subscriptions ADD COLUMN IF NOT EXISTS cold_start_pending boolean NOT NULL DEFAULT false;
