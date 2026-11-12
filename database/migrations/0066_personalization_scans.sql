CREATE TABLE personalization_scans (
  article_id text NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
  input_revision integer NOT NULL,
  scanned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (article_id, input_revision)
);
