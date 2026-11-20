CREATE TABLE label_model_attempts (
  kind text NOT NULL,
  subject text NOT NULL,
  attempted_at timestamptz NOT NULL,
  PRIMARY KEY (kind,subject)
);
