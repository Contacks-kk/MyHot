CREATE INDEX CONCURRENTLY IF NOT EXISTS market_candles_lookup_idx ON market_candles (symbol, timeframe, open_time DESC);
