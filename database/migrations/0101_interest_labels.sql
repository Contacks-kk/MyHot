CREATE TABLE interest_labels (
  id text PRIMARY KEY,
  name text NOT NULL,
  name_key text NOT NULL UNIQUE,
  kind text NOT NULL CHECK (kind IN ('entity','topic')),
  definition text NOT NULL,
  aliases text[] NOT NULL DEFAULT '{}',
  parent_id text REFERENCES interest_labels(id) ON DELETE SET NULL,
  merged_into text REFERENCES interest_labels(id),
  status text NOT NULL CHECK (status IN ('candidate','active','dormant','archived','merged','withdrawn')),
  builtin boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  promoted_at timestamptz,
  last_evidence_at timestamptz
);
