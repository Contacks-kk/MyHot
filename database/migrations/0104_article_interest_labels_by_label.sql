CREATE INDEX CONCURRENTLY IF NOT EXISTS article_interest_labels_by_label_idx ON article_interest_labels(label_id,article_id);
