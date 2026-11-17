CREATE TABLE IF NOT EXISTS source_naming (
  source_id text PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  user_id bigint REFERENCES user_accounts(id) ON DELETE SET NULL,
  status text NOT NULL CHECK (status IN ('awaiting_fetch', 'ready', 'calling', 'received', 'completed', 'failed')),
  candidate text,
  platform_receipt_key text
);
