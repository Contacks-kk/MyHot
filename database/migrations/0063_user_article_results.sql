CREATE TABLE user_article_results (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
  article_id text NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
  input_revision integer NOT NULL,
  model text NOT NULL,
  relevance_score integer NOT NULL CHECK (relevance_score BETWEEN 0 AND 100),
  title text NOT NULL,
  summary text NOT NULL,
  reason text,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, article_id, input_revision)
);
