ALTER TABLE user_source_subscriptions ADD CONSTRAINT user_source_subscriptions_filter_mode_check CHECK (filter_mode IN ('label', 'all')) NOT VALID;
