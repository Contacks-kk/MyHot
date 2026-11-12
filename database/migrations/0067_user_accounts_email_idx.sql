CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS user_accounts_email_lower_idx ON user_accounts (lower(email));
