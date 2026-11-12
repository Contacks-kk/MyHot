CREATE TABLE personalization_rollout (id boolean PRIMARY KEY DEFAULT true CHECK (id), activated_at timestamptz NOT NULL DEFAULT now());
