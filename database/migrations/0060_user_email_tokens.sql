CREATE TABLE user_email_tokens (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  purpose text NOT NULL CHECK (purpose IN ('verify_email', 'password_reset')),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
