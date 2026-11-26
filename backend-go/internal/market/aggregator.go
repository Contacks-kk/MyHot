package market

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"math"
	"sync"
	"time"
)

type barKey struct {
	symbol string
	tf     string
}

// Aggregator turns raw market ticks into aligned multi-timeframe OHLCV bars and quote snapshots.
type Aggregator struct {
	mu       sync.Mutex
	mode     string
	bars     map[barKey]*Candle
	dirty    map[barKey]bool
	quotes   map[string]QuoteSnapshot
	dayOpens map[string]float64
	closed   map[barKey][]Candle
}

/** NewAggregator creates a multi-timeframe candle builder. */
func NewAggregator(mode string) *Aggregator {
	return &Aggregator{
		mode:     mode,
		bars:     make(map[barKey]*Candle),
		dirty:    make(map[barKey]bool),
		quotes:   make(map[string]QuoteSnapshot),
		dayOpens: make(map[string]float64),
		closed:   make(map[barKey][]Candle),
	}
}

/** Seed initializes the aggregator baseline from historical candles so day open and active bars connect seamlessly. */
func (a *Aggregator) Seed(symbol, tf string, candles []Candle) {
	if len(candles) == 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	last := candles[len(candles)-1]
	key := barKey{
		symbol: symbol,
		tf:     tf,
	}

	copied := last
	a.bars[key] = &copied

	if tf == "1d" {
		a.dayOpens[symbol] = last.Open
		if len(candles) >= 2 {
			prevClose := candles[len(candles)-2].Close
			a.quotes[symbol] = buildQuote(symbol, last.Close, last.Open, prevClose, last.High, last.Low, last.Volume, time.Unix(last.Time, 0).UTC(), a.mode)
		}
	}

	if _, exists := a.quotes[symbol]; !exists {
		a.quotes[symbol] = buildQuote(symbol, last.Close, last.Open, last.Open, last.High, last.Low, last.Volume, time.Unix(last.Time, 0).UTC(), a.mode)
	}
}

/** ApplyTick folds one incoming tick across all supported timeframes and records newly closed bars. */
func (a *Aggregator) ApplyTick(tick RawTick) {
	meta, ok := LookupSymbol(tick.Symbol)
	if !ok || tick.Price <= 0 {
		return
	}

	price := roundPrice(tick.Price, meta.Digits)

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, tf := range supportedTimeframes {
		bucketTime, valid := AlignOpenTime(tick.Timestamp, tf)
		if !valid {
			continue
		}

		key := barKey{
			symbol: tick.Symbol,
			tf:     tf,
		}

		existing, exists := a.bars[key]
		if !exists {
			a.bars[key] = &Candle{
				Time:   bucketTime,
				Open:   price,
				High:   price,
				Low:    price,
				Close:  price,
				Volume: tick.Volume,
			}
			a.dirty[key] = true
			continue
		}

		if bucketTime > existing.Time {
			// Roll over and store the completed previous bar for batched persistence.
			a.closed[key] = append(a.closed[key], *existing)
			a.bars[key] = &Candle{
				Time:   bucketTime,
				Open:   price,
				High:   price,
				Low:    price,
				Close:  price,
				Volume: tick.Volume,
			}
			a.dirty[key] = true
			continue
		}

		if bucketTime < existing.Time {
			continue
		}

		if price > existing.High {
			existing.High = price
		}
		if price < existing.Low {
			existing.Low = price
		}
		existing.Close = price
		existing.Volume = math.Round((existing.Volume+tick.Volume)*100) / 100
		a.dirty[key] = true
	}

	dayBar := a.bars[barKey{symbol: tick.Symbol, tf: "1d"}]
	dayOpen := meta.BasePrice
	dayHigh := price
	dayLow := price
	dayVol := tick.Volume
	if dayBar != nil {
		dayOpen = dayBar.Open
		dayHigh = dayBar.High
		dayLow = dayBar.Low
		dayVol = dayBar.Volume
	}

	prevClose := dayOpen
	if prev, hasPrev := a.quotes[tick.Symbol]; hasPrev && prev.PrevClose > 0 {
		prevClose = prev.PrevClose
	}

	a.quotes[tick.Symbol] = buildQuote(tick.Symbol, price, dayOpen, prevClose, dayHigh, dayLow, dayVol, tick.Timestamp, a.mode)
}

/** DrainUpdates extracts dirty live bars and newly finalized bars since the previous flush. */
func (a *Aggregator) DrainUpdates() (updates []TickUpdate, closed map[barKey][]Candle, quotes map[string]QuoteSnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.dirty) > 0 {
		updates = make([]TickUpdate, 0, len(a.dirty))
		quotes = make(map[string]QuoteSnapshot, len(a.quotes))

		for sym, q := range a.quotes {
			quotes[sym] = q
		}

		for key := range a.dirty {
			bar, exists := a.bars[key]
			if !exists {
				continue
			}

			q := quotes[key.symbol]
			qCopy := q
			updates = append(updates, TickUpdate{
				Type:      "candle",
				Symbol:    key.symbol,
				Timeframe: key.tf,
				Candle:    *bar,
				Quote:     &qCopy,
			})
		}

		a.dirty = make(map[barKey]bool)
	}

	if len(a.closed) > 0 {
		closed = a.closed
		a.closed = make(map[barKey][]Candle)
	}

	return updates, closed, quotes
}

func buildQuote(symbol string, price, open, prevClose, high, low, volume float64, ts time.Time, mode string) QuoteSnapshot {
	ref := prevClose
	if ref <= 0 {
		ref = open
	}

	change := price - ref
	var changePct float64
	if ref > 0 {
		changePct = (change / ref) * 100
	}

	return QuoteSnapshot{
		Symbol:    symbol,
		Price:     price,
		Open:      open,
		PrevClose: ref,
		High:      high,
		Low:       low,
		Change:    math.Round(change*100) / 100,
		ChangePct: math.Round(changePct*100) / 100,
		Volume:    volume,
		UpdatedAt: ts.Unix(),
		Mode:      mode,
	}
}

// Collector runs inside go-worker to maintain a single leased upstream feed and sync Redis/PostgreSQL.
type Collector struct {
	repo       *Repository
	redis      *RedisStore
	provider   Provider
	logger     *slog.Logger
	ownerID    string
	aggregator *Aggregator
}

/** NewCollector builds a supervised market collector worker. */
func NewCollector(repo *Repository, redis *RedisStore, provider Provider, logger *slog.Logger) *Collector {
	if logger == nil {
		logger = slog.Default()
	}

	return &Collector{
		repo:       repo,
		redis:      redis,
		provider:   provider,
		logger:     logger,
		ownerID:    randomOwnerID(),
		aggregator: NewAggregator(provider.Mode()),
	}
}

/** Run coordinates distributed leader locking, historical backfill, and throttled real-time aggregation until ctx ends. */
func (c *Collector) Run(ctx context.Context) {
	if c.provider == nil {
		return
	}

	// Retry leader acquisition so standby workers take over smoothly if the active collector stops.
	for ctx.Err() == nil {
		acquired, err := c.redis.AcquireCollectorLock(ctx, c.ownerID, 15*time.Second)
		if err != nil {
			c.logger.Warn("market collector lock error", "error", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		if !acquired {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		c.runAsLeader(ctx)
		c.redis.ReleaseCollectorLock(context.Background(), c.ownerID)
	}
}

func (c *Collector) runAsLeader(ctx context.Context) {
	leaderCtx, cancelLeader := context.WithCancel(ctx)
	defer cancelLeader()

	symbols := make([]string, 0, len(supportedSymbols))
	for _, meta := range supportedSymbols {
		symbols = append(symbols, meta.Symbol)
	}

	// Seed history from provider or PostgreSQL before starting real-time stream.
	c.seedInitialCandles(leaderCtx, symbols)

	// Maintain Redis lease renewal while active.
	go func() {
		renewTicker := time.NewTicker(5 * time.Second)
		defer renewTicker.Stop()

		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-renewTicker.C:
				ok, err := c.redis.RenewCollectorLock(leaderCtx, c.ownerID, 15*time.Second)
				if err != nil || !ok {
					c.logger.Warn("lost market collector lock, pausing ingestion")
					cancelLeader()
					return
				}
			}
		}
	}()

	ticks := make(chan RawTick, 256)

	// Run upstream stream loop with exponential backoff on disconnect.
	go func() {
		backoff := time.Second
		for leaderCtx.Err() == nil {
			started := time.Now()
			err := c.provider.Stream(leaderCtx, symbols, ticks)
			if leaderCtx.Err() != nil {
				return
			}

			if err != nil {
				c.logger.Warn("market upstream stream disconnected", "provider", c.provider.Name(), "error", err.Error())
			}

			if time.Since(started) > 30*time.Second {
				backoff = time.Second
			}

			select {
			case <-leaderCtx.Done():
				return
			case <-time.After(backoff):
			}

			backoff = min(backoff*2, 30*time.Second)
		}
	}()

	flushTicker := time.NewTicker(300 * time.Millisecond)
	defer flushTicker.Stop()

	for {
		select {
		case <-leaderCtx.Done():
			c.flushAggregator(context.Background())
			return
		case tick := <-ticks:
			c.aggregator.ApplyTick(tick)
		case <-flushTicker.C:
			c.flushAggregator(leaderCtx)
		}
	}
}

func (c *Collector) seedInitialCandles(ctx context.Context, symbols []string) {
	for _, symbol := range symbols {
		for _, tf := range supportedTimeframes {
			if ctx.Err() != nil {
				return
			}

			history, err := c.provider.FetchHistory(ctx, symbol, tf, DefaultCandleLimit)
			if err != nil {
				c.logger.Warn("market history fetch failed", "symbol", symbol, "tf", tf, "error", err.Error())
				continue
			}

			if len(history) == 0 {
				continue
			}

			c.aggregator.Seed(symbol, tf, history)

			if err := c.repo.UpsertCandles(ctx, symbol, tf, history); err != nil {
				c.logger.Warn("market history pg upsert failed", "symbol", symbol, "tf", tf, "error", err.Error())
			}

			if err := c.redis.SaveWindowCandles(ctx, symbol, tf, history); err != nil {
				c.logger.Warn("market history redis cache failed", "symbol", symbol, "tf", tf, "error", err.Error())
			}
		}
	}

	c.flushAggregator(ctx)
}

func (c *Collector) flushAggregator(ctx context.Context) {
	updates, closed, quotes := c.aggregator.DrainUpdates()

	for symbol, q := range quotes {
		if err := c.redis.SaveQuote(ctx, q); err != nil {
			c.logger.Warn("market save quote failed", "symbol", symbol, "error", err.Error())
		}
	}

	for key, bars := range closed {
		if err := c.repo.UpsertCandles(ctx, key.symbol, key.tf, bars); err != nil {
			c.logger.Warn("market closed candle upsert failed", "symbol", key.symbol, "tf", key.tf, "error", err.Error())
		}

		if err := c.redis.SaveWindowCandles(ctx, key.symbol, key.tf, bars); err != nil {
			c.logger.Warn("market closed candle redis cache failed", "symbol", key.symbol, "tf", key.tf, "error", err.Error())
		}
	}

	for _, update := range updates {
		if err := c.redis.SetLiveCandle(ctx, update.Symbol, update.Timeframe, update.Candle); err != nil {
			c.logger.Warn("market live candle cache failed", "symbol", update.Symbol, "tf", update.Timeframe, "error", err.Error())
		}

		if err := c.redis.PublishTick(ctx, update); err != nil {
			c.logger.Warn("market publish tick failed", "symbol", update.Symbol, "tf", update.Timeframe, "error", err.Error())
		}
	}
}

func randomOwnerID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().UTC().Format("20060102150405")
	}
	return hex.EncodeToString(buf)
}
