package market

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"
)

// ProviderConfig holds credentials and provider selection for market ingestion.
type ProviderConfig struct {
	Name          string
	TwelveDataKey string
	FinnhubKey    string
}

/** NewProvider builds the explicitly configured provider. */
func NewProvider(cfg ProviderConfig, logger *slog.Logger) Provider {
	if logger == nil {
		logger = slog.Default()
	}

	switch cfg.Name {
	case "mock":
		return NewMockProvider()
	case "twelvedata":
		if cfg.TwelveDataKey != "" {
			return NewTwelveDataProvider(cfg.TwelveDataKey, logger)
		}

		logger.Warn("MARKET_PROVIDER=twelvedata selected without MARKET_TWELVEDATA_API_KEY")
	case "finnhub":
		if cfg.FinnhubKey != "" {
			return NewFinnhubProvider(cfg.FinnhubKey, logger)
		}

		logger.Warn("MARKET_PROVIDER=finnhub selected without MARKET_FINNHUB_API_KEY")
	default:
		logger.Warn("unsupported market provider")
	}

	return nil
}

// MockProvider generates coherent historical and 500ms simulated real-time ticks for development and offline testing.
type MockProvider struct {
	mu     sync.Mutex
	prices map[string]float64
	rng    *rand.Rand
}

/** NewMockProvider initializes a simulated market data provider seeded around asset reference prices. */
func NewMockProvider() *MockProvider {
	prices := make(map[string]float64, len(supportedSymbols))
	for _, meta := range supportedSymbols {
		prices[meta.Symbol] = meta.BasePrice
	}

	return &MockProvider{
		prices: prices,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

/** Name returns "mock". */
func (p *MockProvider) Name() string {
	return "mock"
}

/** Mode returns "mock". */
func (p *MockProvider) Mode() string {
	return "mock"
}

/** FetchHistory synthesizes aligned historical candles ending near the current UTC time. */
func (p *MockProvider) FetchHistory(ctx context.Context, symbol string, tf string, limit int) ([]Candle, error) {
	meta, ok := LookupSymbol(symbol)
	if !ok {
		return nil, nil
	}

	stepSec, validTF := TimeframeSeconds(tf)
	if !validTF {
		return nil, nil
	}

	if limit <= 0 || limit > MaxCandleLimit {
		limit = DefaultCandleLimit
	}

	nowOpen, _ := AlignOpenTime(time.Now().UTC(), tf)
	nowStart := time.Unix(nowOpen, 0).UTC()

	p.mu.Lock()
	defer p.mu.Unlock()

	price := meta.BasePrice * 0.992
	if cur, exists := p.prices[symbol]; exists && tf == "1m" {
		// Walk backward gently so minute history meets the active baseline smoothly.
		price = cur * 0.996
	}

	scale := meta.BasePrice * 0.0008
	meanRevert := 0.015
	switch tf {
	case "1h":
		scale = meta.BasePrice * 0.0022
	case "1d":
		scale = meta.BasePrice * 0.0065
	case "1w":
		scale = meta.BasePrice * 0.015
		meanRevert = 0.03
	case "1M":
		scale = meta.BasePrice * 0.032
		meanRevert = 0.06
	}

	candles := make([]Candle, 0, limit)
	for i := 0; i < limit; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		offset := -(limit - 1) + i
		var openTime int64
		switch tf {
		case "1M":
			openTime = nowStart.AddDate(0, offset, 0).Unix()
		case "1w":
			openTime = nowStart.AddDate(0, 0, offset*7).Unix()
		case "1d":
			openTime = nowStart.AddDate(0, 0, offset).Unix()
		default:
			openTime = nowOpen + int64(offset)*stepSec
		}
		open := roundPrice(price, meta.Digits)

		// Mean-revert subtly around BasePrice so long simulations stay realistic.
		drift := (meta.BasePrice - open) * meanRevert
		delta := (p.rng.Float64()-0.48)*scale*2 + drift
		closePrice := math.Max(meta.BasePrice*0.45, roundPrice(open+delta, meta.Digits))

		wickUp := p.rng.Float64() * scale * 0.8
		wickDown := p.rng.Float64() * scale * 0.8
		high := roundPrice(math.Max(open, closePrice)+wickUp, meta.Digits)
		low := roundPrice(math.Max(0.01, math.Min(open, closePrice)-wickDown), meta.Digits)
		volume := math.Round((50+p.rng.Float64()*450)*100) / 100

		candles = append(candles, Candle{
			Time:   openTime,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closePrice,
			Volume: volume,
		})

		price = closePrice
	}

	if len(candles) > 0 && tf == "1m" {
		p.prices[symbol] = candles[len(candles)-1].Close
	}

	return candles, nil
}

/** Stream emits simulated real-time market ticks every 500ms until context cancellation. */
func (p *MockProvider) Stream(ctx context.Context, symbols []string, out chan<- RawTick) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			for _, symbol := range symbols {
				meta, ok := LookupSymbol(symbol)
				if !ok {
					continue
				}

				p.mu.Lock()
				cur := p.prices[symbol]
				if cur <= 0 {
					cur = meta.BasePrice
				}

				step := meta.BasePrice * 0.00025
				revert := (meta.BasePrice - cur) * 0.008
				next := cur + (p.rng.Float64()*2-1)*step + revert
				next = roundPrice(math.Max(meta.BasePrice*0.5, next), meta.Digits)
				p.prices[symbol] = next
				vol := math.Round((1+p.rng.Float64()*12)*100) / 100
				p.mu.Unlock()

				tick := RawTick{
					Symbol:    symbol,
					Price:     next,
					Volume:    vol,
					Timestamp: now.UTC(),
				}

				select {
				case out <- tick:
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
}

func roundPrice(val float64, digits int) float64 {
	factor := math.Pow(10, float64(digits))
	return math.Round(val*factor) / factor
}
