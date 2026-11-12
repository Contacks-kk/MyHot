CREATE TABLE user_accounts (
  id bigserial PRIMARY KEY,
  email text NOT NULL,
  password_hash text NOT NULL,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
