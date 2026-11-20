CREATE TABLE article_label_index_state (
  article_id text PRIMARY KEY REFERENCES articles(id) ON DELETE CASCADE,
  revision integer NOT NULL,
  catalog_version text NOT NULL
);
