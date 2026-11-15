ALTER TABLE user_source_subscriptions ADD COLUMN IF NOT EXISTS filter_mode text NOT NULL DEFAULT 'label';
