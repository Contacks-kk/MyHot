CREATE TABLE user_model_configs (
  user_id bigint PRIMARY KEY REFERENCES user_accounts(id) ON DELETE CASCADE,
  base_url text NOT NULL,
  api_key_ciphertext text NOT NULL,
  model text NOT NULL,
  config_version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now()
);
