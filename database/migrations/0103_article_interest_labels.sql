CREATE TABLE article_interest_labels (
  article_id text NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
  label_id text NOT NULL REFERENCES interest_labels(id) ON DELETE CASCADE,
  PRIMARY KEY (article_id,label_id)
);
