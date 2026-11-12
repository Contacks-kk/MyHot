CREATE TABLE user_interests (
  user_id bigint PRIMARY KEY REFERENCES user_accounts(id) ON DELETE CASCADE,
  keywords text[] NOT NULL DEFAULT '{}',
  entity_ids text[] NOT NULL DEFAULT '{}',
  preference_version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now()
);
