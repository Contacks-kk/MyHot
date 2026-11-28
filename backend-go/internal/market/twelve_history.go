package market

import (
	"context"
	"errors"
	"slices"
	"time"
	_ "time/tzdata"
)

var newYork = func() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return location
}()

type twelveHistoryBatch struct {
	barKey
	bars []Candle
}

/** pollTwelveHistory fills the initial windows and performs a shared incremental refresh. */
func (c *Collector) pollTwelveHistory(ctx context.Context, symbols []string, out chan<- twelveHistoryBatch) {
	for _, tf := range supportedTimeframes {
		if !c.fetchTwelveTimeframe(ctx, symbols, tf, DefaultCandleLimit, out) {
			return
		}
	}

	for ctx.Err() == nil {
		next := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		for _, tf := range supportedTimeframes {
			if !c.fetchTwelveTimeframe(ctx, twelvePollSymbols(next, symbols, tf), tf, twelvePollLimit(tf), out) {
				return
			}
		}
	}
}

/** twelvePollLimit sizes overlapping windows without truncating cached weekly or monthly history. */
func twelvePollLimit(tf string) int {
	switch tf {
	case "1m", "5m", "1h":
		return 60
	default:
		return DefaultCandleLimit
	}
}

/** twelvePollSymbols schedules closed-bar polling after a short upstream processing window. */
func twelvePollSymbols(now time.Time, symbols []string, tf string) []string {
	minute := now.UTC().Minute()
	switch tf {
	case "1m":
		if minute%5 != 2 {
			return nil
		}
	case "5m":
		if minute%15 != 2 {
			return nil
		}
	case "1h":
		if minute != 2 {
			return nil
		}
	default:
		if now.UTC().Hour() != 1 || minute != 2 {
			return nil
		}
		return symbols
	}

	et := now.In(newYork)
	minutesSinceMidnight := et.Hour()*60 + et.Minute()
	if et.Weekday() != time.Saturday && et.Weekday() != time.Sunday &&
		minutesSinceMidnight >= 9*60+30 && minutesSinceMidnight <= 16*60+2 {
		return symbols
	}
	if slices.Contains(symbols, "XAU/USD") {
		return []string{"XAU/USD"}
	}
	return nil
}

/** fetchTwelveTimeframe charges each attempt to the shared budget before calling Twelve Data. */
func (c *Collector) fetchTwelveTimeframe(ctx context.Context, symbols []string, tf string, limit int, out chan<- twelveHistoryBatch) bool {
	for _, symbol := range symbols {
		bars, err := c.fetchBudgetedHistory(ctx, symbol, tf, limit)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			c.logger.Warn("market history fetch failed", "provider", c.provider.Name(), "symbol", symbol, "tf", tf, "error", err.Error())
			continue
		}
		if tf == "1m" {
			closedBefore := time.Now().UTC().Truncate(time.Minute).Unix()
			bars = slices.DeleteFunc(bars, func(bar Candle) bool { return bar.Time >= closedBefore })
		}
		if len(bars) == 0 {
			continue
		}

		select {
		case out <- twelveHistoryBatch{
			barKey: barKey{
				symbol: symbol,
				tf:     tf,
			},
			bars: bars,
		}:
		case <-ctx.Done():
			return false
		}
	}
	return ctx.Err() == nil
}

/** fetchBudgetedHistory retries one upstream 429 after its minute window has passed. */
func (c *Collector) fetchBudgetedHistory(ctx context.Context, symbol, tf string, limit int) ([]Candle, error) {
	for attempt := 0; ; attempt++ {
		reserved, err := c.redis.ReserveHistoryCredit(ctx)
		if err != nil || !reserved {
			return nil, err
		}
		bars, err := c.provider.FetchHistory(ctx, symbol, tf, limit)
		if attempt > 0 || !errors.Is(err, errTwelveRateLimited) {
			return bars, err
		}

		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

/** saveTwelveHistory persists upstream bars and tells the browser to reload the corrected window. */
func (c *Collector) saveTwelveHistory(ctx context.Context, batch twelveHistoryBatch) {
	symbol, tf := batch.symbol, batch.tf
	if err := c.repo.UpsertCandles(ctx, symbol, tf, batch.bars); err != nil {
		c.logger.Warn("market history pg upsert failed", "symbol", symbol, "tf", tf, "error", err.Error())
		return
	}

	// Reconcile before publishing the snapshot , so an older live tick cannot replace a finalized bar.
	quote := c.aggregator.reconcileTwelveHistory(batch)

	// inserts closed candles into a bounded Redis ZSET indexed by UTC open time.
	if err := c.redis.SaveWindowCandles(ctx, symbol, tf, batch.bars); err != nil {
		c.logger.Warn("market history redis cache failed", "symbol", symbol, "tf", tf, "error", err.Error())
		return
	}
	if quote != nil {
		if err := c.redis.SaveQuote(ctx, *quote); err != nil {
			c.logger.Warn("market save quote failed", "symbol", symbol, "error", err.Error())
		}
	}
	if err := c.redis.PublishTick(ctx, TickUpdate{
		Type:      "snapshot",
		Symbol:    symbol,
		Timeframe: tf,
		Quote:     quote,
	}); err != nil {
		c.logger.Warn("market publish snapshot failed", "symbol", symbol, "tf", tf, "error", err.Error())
	}
}

/** reconcileTwelveHistory keeps the latest upstream minute authoritative and refreshes the quote(price). */
func (a *Aggregator) reconcileTwelveHistory(batch twelveHistoryBatch) *QuoteSnapshot {
	symbol := batch.symbol
	if batch.tf == "1d" {
		a.dailyHistory[symbol] = batch.bars[max(0, len(batch.bars)-2):]
	}
	if batch.tf != "1m" {
		return nil
	}
	bar := batch.bars[len(batch.bars)-1]
	if bar.Time < a.officialThrough[symbol] {
		return nil
	}
	a.officialThrough[symbol] = bar.Time
	if live := a.bars[batch.barKey]; live != nil && live.Time <= bar.Time {
		delete(a.bars, batch.barKey)
		delete(a.dirty, batch.barKey)
	}

	closeTime := time.Unix(bar.Time+60, 0)
	previousQuote := a.quotes[symbol]
	if previousQuote.Mode == "live" && previousQuote.UpdatedAt >= closeTime.Unix() {
		return nil
	}
	quote := a.twelveQuote(symbol, bar, closeTime, "rest")
	a.quotes[symbol] = quote
	return &quote
}

/** applyTwelveTick updates only the provisional current minute. */
func (a *Aggregator) applyTwelveTick(tick RawTick, now time.Time) {
	if tick.Price <= 0 || tick.Timestamp.After(now.Add(5*time.Second)) ||
		tick.Timestamp.Unix() < a.quotes[tick.Symbol].UpdatedAt {
		return
	}

	bucket := tick.Timestamp.Truncate(time.Minute).Unix()
	if bucket != now.Truncate(time.Minute).Unix() || bucket <= a.officialThrough[tick.Symbol] {
		return
	}
	key := barKey{
		symbol: tick.Symbol,
		tf:     "1m",
	}
	bar := a.bars[key]
	if bar == nil || bar.Time < bucket {
		bar = &Candle{
			Time: bucket,
			Open: tick.Price,
			High: tick.Price,
			Low:  tick.Price,
		}
		a.bars[key] = bar
	}
	bar.High = max(bar.High, tick.Price)
	bar.Low = min(bar.Low, tick.Price)
	bar.Close = tick.Price
	a.quotes[tick.Symbol] = a.twelveQuote(tick.Symbol, *bar, tick.Timestamp, "live")
	a.dirty[key] = true
}

/** twelveQuote compares the latest price with the previous official trading-day close. */
func (a *Aggregator) twelveQuote(symbol string, bar Candle, updated time.Time, mode string) QuoteSnapshot {
	daily := a.dailyHistory[symbol]
	prevClose := 0.0
	localDate := time.Unix(bar.Time, 0).In(newYork).Format(time.DateOnly)
	if len(daily) > 0 {
		last := daily[len(daily)-1]
		if time.Unix(last.Time, 0).UTC().Format(time.DateOnly) < localDate {
			prevClose = last.Close
		} else if len(daily) > 1 {
			prevClose = daily[len(daily)-2].Close
		}
	}
	quote := buildQuote(symbol, bar.Close, bar.Open, prevClose, bar.High, bar.Low, bar.Volume, updated, mode)
	if prevClose == 0 {
		quote.PrevClose = 0
		quote.Change = 0
		quote.ChangePct = 0
	}
	return quote
}
