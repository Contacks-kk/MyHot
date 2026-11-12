CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS sources_collector_identity_idx ON sources (collector_identity) WHERE collector_identity IS NOT NULL;
