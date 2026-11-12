CREATE TABLE user_sessions (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
