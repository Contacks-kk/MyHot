ALTER TABLE user_interests ADD COLUMN label_ids text[] NOT NULL DEFAULT '{}';
