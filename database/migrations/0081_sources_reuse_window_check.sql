ALTER TABLE sources ADD CONSTRAINT sources_reuse_window_minutes_check CHECK (reuse_window_minutes BETWEEN 1 AND 1440) NOT VALID;
