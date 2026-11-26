CREATE TABLE IF NOT EXISTS market_candles (
  symbol text NOT NULL,
  timeframe text NOT NULL CHECK (timeframe IN ('1m', '5m', '1h', '1d')),
  open_time timestamptz NOT NULL,
  open double precision NOT NULL,
  high double precision NOT NULL,
  low double precision NOT NULL,
  close double precision NOT NULL,
  volume double precision NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (symbol, timeframe, open_time)
);
