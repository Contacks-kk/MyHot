CREATE TABLE user_daily_reports (
  user_id bigint NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
  report_date date NOT NULL,
  model text NOT NULL,
  title text NOT NULL,
  summary text NOT NULL,
  entries jsonb NOT NULL DEFAULT '[]',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, report_date)
);
