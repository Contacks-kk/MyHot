CREATE TABLE user_model_call_receipts (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
  logical_key text NOT NULL UNIQUE,
  purpose text NOT NULL CHECK (purpose IN ('article', 'daily', 'connection_test')),
  article_id text REFERENCES articles(id) ON DELETE SET NULL,
  model text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'completed', 'failed')),
  request_hash text NOT NULL,
  usage jsonb,
  error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
