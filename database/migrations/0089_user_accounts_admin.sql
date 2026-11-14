ALTER TABLE user_accounts ADD COLUMN IF NOT EXISTS is_admin boolean NOT NULL DEFAULT false;
