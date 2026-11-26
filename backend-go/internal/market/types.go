package market

import (
	"context"
	"time"
)

const (
	// DefaultCandleLimit defines the default number of candles loaded for initial chart rendering.
	DefaultCandleLimit = 300

	// MaxCandleLimit caps single-request candle retrieval size to protect API and database latency.
	MaxCandleLimit = 500
)

// SymbolMeta describes a supported market asset and its news matching metadata.
type SymbolMeta struct {
	Symbol      string   `json:"symbol"`
	Name        string   `json:"name"`
	ZhName      string   `json:"zhName"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Exchange    string   `json:"exchange"`
	Digits      int      `json:"digits"`
	BasePrice   float64  `json:"-"`
	Keywords    []string `json:"-"`
}

// Candle represents a single OHLCV bar aligned to a UTC Unix timestamp in seconds.
type Candle struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// QuoteSnapshot represents the latest summary quote for one tracked market asset.
type QuoteSnapshot struct {
	Symbol    string  `json:"symbol"`
	Price     float64 `json:"price"`
	Open      float64 `json:"open"`
	PrevClose float64 `json:"prevClose"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Change    float64 `json:"change"`
	ChangePct float64 `json:"changePct"`
	Volume    float64 `json:"volume"`
	UpdatedAt int64   `json:"updatedAt"`
	Mode      string  `json:"mode"`
}

// NewsMarker represents an editorial or personal article aligned to a candlestick bar.
type NewsMarker struct {
	Time   int64   `json:"time"`
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Score  float64 `json:"score"`
	Source string  `json:"source"`
	Scope  string  `json:"scope"`
	Href   string  `json:"href"`
}

// MarketsCatalogResponse holds asset definitions, timeframes, quotes, and runtime status.
type MarketsCatalogResponse struct {
	Enabled    bool                     `json:"enabled"`
	Provider   string                   `json:"provider"`
	Mode       string                   `json:"mode"`
	Timeframes []string                 `json:"timeframes"`
	Symbols    []SymbolMeta             `json:"symbols"`
	Quotes     map[string]QuoteSnapshot `json:"quotes"`
}

// CandlesResponse holds the merged historical and live candle window alongside news markers.
type CandlesResponse struct {
	Symbol    string         `json:"symbol"`
	Timeframe string         `json:"timeframe"`
	Mode      string         `json:"mode"`
	Quote     *QuoteSnapshot `json:"quote,omitempty"`
	Candles   []Candle       `json:"candles"`
	Markers   []NewsMarker   `json:"markers"`
}

// TickUpdate encodes a throttled real-time candle and quote broadcast packet over Redis and WebSocket.
type TickUpdate struct {
	Type      string         `json:"type"`
	Symbol    string         `json:"symbol"`
	Timeframe string         `json:"tf"`
	Candle    Candle         `json:"candle"`
	Quote     *QuoteSnapshot `json:"quote,omitempty"`
}

// WSClientMessage decodes inbound WebSocket control frames from browser clients.
type WSClientMessage struct {
	Op     string `json:"op"`
	Symbol string `json:"symbol,omitempty"`
	TF     string `json:"tf,omitempty"`
}

// RawTick represents a normalized trade or quote tick emitted by an upstream provider.
type RawTick struct {
	Symbol    string
	Price     float64
	Volume    float64
	Timestamp time.Time
}

// Provider defines the pluggable upstream market history and streaming adapter contract.
type Provider interface {
	/** Name returns the stable identifier of the provider implementation. */
	Name() string
	/** Mode returns whether the provider delivers live or simulated market quotes. */
	Mode() string
	/** FetchHistory fetches historical OHLCV bars ordered ascending by open time. */
	FetchHistory(ctx context.Context, symbol string, tf string, limit int) ([]Candle, error)
	/** Stream continuously emits real-time raw ticks until context cancellation or fatal error. */
	Stream(ctx context.Context, symbols []string, out chan<- RawTick) error
}

var supportedSymbols = []SymbolMeta{
	{
		Symbol:      "XAU/USD",
		Name:        "Gold Spot",
		ZhName:      "现货黄金",
		Description: "国际伦敦现货黄金 / 美元代理报价",
		Category:    "贵金属",
		Exchange:    "FX / OTC",
		Digits:      2,
		BasePrice:   2654.40,
		Keywords:    []string{"黄金", "金价", "gold", "xau", "bullion", "美联储", "fed", "降息", "通胀", "cpi", "避险"},
	},
	{
		Symbol:      "QQQ",
		Name:        "Invesco QQQ",
		ZhName:      "纳斯达克 100 追踪",
		Description: "纳斯达克 100 指数核心追踪 ETF（代表科技与 AI 权重）",
		Category:    "美股指数",
		Exchange:    "NASDAQ",
		Digits:      2,
		BasePrice:   508.60,
		Keywords:    []string{"纳斯达克", "nasdaq", "qqq", "英伟达", "nvidia", "openai", "微软", "microsoft", "苹果", "apple", "谷歌", "google", "meta", "anthropic", "算力", "芯片", "半导体"},
	},
	{
		Symbol:      "SPY",
		Name:        "SPDR S&P 500",
		ZhName:      "标普 500 追踪",
		Description: "标普 500 指数核心追踪 ETF（反映美国大盘整体走势）",
		Category:    "美股指数",
		Exchange:    "NYSE Arca",
		Digits:      2,
		BasePrice:   596.25,
		Keywords:    []string{"标普", "s&p", "spy", "美股", "美联储", "fed", "非农", "通胀", "cpi", "gdp", "降息", "财报", "宏观"},
	},
}

var supportedTimeframes = []string{"1m", "5m", "1h", "1d", "1w", "1M"}

/** Catalog returns an immutable copy of supported market asset metadata. */
func Catalog() []SymbolMeta {
	out := make([]SymbolMeta, len(supportedSymbols))
	copy(out, supportedSymbols)
	return out
}

/** SupportedTimeframes returns the supported candlestick intervals in UI display order. */
func SupportedTimeframes() []string {
	out := make([]string, len(supportedTimeframes))
	copy(out, supportedTimeframes)
	return out
}

/** LookupSymbol validates and returns metadata for a supported asset symbol. */
func LookupSymbol(symbol string) (SymbolMeta, bool) {
	for _, meta := range supportedSymbols {
		if meta.Symbol == symbol {
			return meta, true
		}
	}
	return SymbolMeta{}, false
}

/** IsValidTimeframe checks whether a timeframe string matches supported chart intervals. */
func IsValidTimeframe(tf string) bool {
	for _, item := range supportedTimeframes {
		if item == tf {
			return true
		}
	}
	return false
}

/** IsPersistedTimeframe checks whether a timeframe is stored directly as raw rows in market_candles. */
func IsPersistedTimeframe(tf string) bool {
	return tf == "1m" || tf == "5m" || tf == "1h" || tf == "1d"
}

/** TimeframeSeconds converts a supported timeframe string into nominal bucket width in seconds. */
func TimeframeSeconds(tf string) (int64, bool) {
	switch tf {
	case "1m":
		return 60, true
	case "5m":
		return 300, true
	case "1h":
		return 3600, true
	case "1d":
		return 86400, true
	case "1w":
		return 7 * 86400, true
	case "1M":
		return 30 * 86400, true
	default:
		return 0, false
	}
}

/** AlignOpenTime maps an arbitrary timestamp onto the UTC start epoch second of a timeframe bucket. */
func AlignOpenTime(t time.Time, tf string) (int64, bool) {
	u := t.UTC()

	switch tf {
	case "1m":
		return u.Truncate(time.Minute).Unix(), true
	case "5m":
		return u.Truncate(5 * time.Minute).Unix(), true
	case "1h":
		return u.Truncate(time.Hour).Unix(), true
	case "1d":
		dayStart := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		return dayStart.Unix(), true
	case "1w":
		daysSinceMonday := (int(u.Weekday()) + 6) % 7
		weekStart := time.Date(u.Year(), u.Month(), u.Day()-daysSinceMonday, 0, 0, 0, 0, time.UTC)
		return weekStart.Unix(), true
	case "1M":
		monthStart := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
		return monthStart.Unix(), true
	default:
		return 0, false
	}
}

/** NextOpenTime returns the exclusive UTC end epoch second for a bucket starting at openSec. */
func NextOpenTime(openSec int64, tf string) int64 {
	start := time.Unix(openSec, 0).UTC()
	switch tf {
	case "1m":
		return start.Add(time.Minute).Unix()
	case "5m":
		return start.Add(5 * time.Minute).Unix()
	case "1h":
		return start.Add(time.Hour).Unix()
	case "1d":
		return start.AddDate(0, 0, 1).Unix()
	case "1w":
		return start.AddDate(0, 0, 7).Unix()
	case "1M":
		return start.AddDate(0, 1, 0).Unix()
	default:
		return openSec + 60
	}
}

/** SubscriptionKey builds the canonical channel key pairing symbol and timeframe. */
func SubscriptionKey(symbol, tf string) string {
	return symbol + "|" + tf
}
