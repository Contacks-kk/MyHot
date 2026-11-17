ALTER TABLE user_source_subscriptions ADD COLUMN IF NOT EXISTS name_inherited boolean NOT NULL DEFAULT false;
