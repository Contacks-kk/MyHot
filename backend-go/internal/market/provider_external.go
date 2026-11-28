package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// TwelveDataProvider adapts Twelve Data REST history and WebSocket live price feeds.
type TwelveDataProvider struct {
	apiKey string
	http   *http.Client
	logger *slog.Logger
}

var (
	errTwelveSubscriptionsDenied = errors.New("twelvedata denied all price subscriptions")
	errTwelveRateLimited         = errors.New("twelvedata rate limit (429)")
)

/** NewTwelveDataProvider constructs a Twelve Data market provider. */
func NewTwelveDataProvider(apiKey string, logger *slog.Logger) *TwelveDataProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &TwelveDataProvider{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 15 * time.Second},
		logger: logger,
	}
}

/** Name returns "twelvedata". */
func (p *TwelveDataProvider) Name() string {
	return "twelvedata"
}

/** Mode returns "live". */
func (p *TwelveDataProvider) Mode() string {
	return "live"
}

// FetchHistory /** FetchHistory retrieves historical candles via Twelve Data time_series endpoint. */
func (p *TwelveDataProvider) FetchHistory(ctx context.Context, symbol, tf string, limit int) ([]Candle, error) {
	interval, ok := twelveDataInterval(tf)
	if !ok {
		return nil, fmt.Errorf("unsupported timeframe: %s", tf)
	}

	if limit <= 0 || limit > MaxCandleLimit {
		limit = DefaultCandleLimit
	}

	query := url.Values{
		"symbol":     {symbol},
		"interval":   {interval},
		"outputsize": {strconv.Itoa(limit)},
		"timezone":   {"UTC"},
		"apikey":     {p.apiKey},
	}

	endpoint := "https://api.twelvedata.com/time_series?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("twelvedata history request failed (%T)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errTwelveRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twelvedata history status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Status string `json:"status"`
		Code   int    `json:"code"`
		Values []struct {
			Datetime string `json:"datetime"`
			Open     string `json:"open"`
			High     string `json:"high"`
			Low      string `json:"low"`
			Close    string `json:"close"`
			Volume   string `json:"volume"`
		} `json:"values"`
	}

	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	if parsed.Status == "error" {
		if parsed.Code == http.StatusTooManyRequests {
			return nil, errTwelveRateLimited
		}
		return nil, fmt.Errorf("twelvedata API error code %d", parsed.Code)
	}

	out := make([]Candle, 0, len(parsed.Values))
	seen := make(map[int64]bool, len(parsed.Values))
	for i := len(parsed.Values) - 1; i >= 0; i-- {
		row := parsed.Values[i]
		ts, parseErr := parseTwelveDataTime(row.Datetime, tf)
		if parseErr != nil || seen[ts] {
			continue
		}

		open, err1 := strconv.ParseFloat(row.Open, 64)
		high, err2 := strconv.ParseFloat(row.High, 64)
		low, err3 := strconv.ParseFloat(row.Low, 64)
		closeVal, err4 := strconv.ParseFloat(row.Close, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}

		seen[ts] = true
		vol, _ := strconv.ParseFloat(row.Volume, 64)
		out = append(out, Candle{
			Time:   ts,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closeVal,
			Volume: vol,
		})
	}

	return out, nil
}

// Stream /** Stream maintains a WebSocket subscription to Twelve Data real-time price updates. */
func (p *TwelveDataProvider) Stream(ctx context.Context, symbols []string, out chan<- RawTick) error {
	endpoint := "wss://ws.twelvedata.com/v1/quotes/price?apikey=" + url.QueryEscape(p.apiKey)
	dialCtx, cancelDial := context.WithTimeout(ctx, 15*time.Second)

	// build long-lived WebSocket connection
	conn, resp, err := websocket.Dial(dialCtx, endpoint, nil)
	cancelDial()
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return errTwelveSubscriptionsDenied
		}
		return fmt.Errorf("twelvedata dial failed (%T)", err)
	}
	defer conn.CloseNow()

	subMsg := map[string]any{
		"action": "subscribe",
		"params": map[string]any{
			"symbols": strings.Join(symbols, ","),
		},
	}

	writeCtx, cancelWrite := context.WithTimeout(ctx, 10*time.Second)
	defer cancelWrite()

	// send subscription message
	if err := wsjson.Write(writeCtx, conn, subMsg); err != nil {
		return fmt.Errorf("twelvedata subscription write failed (%T)", err)
	}

	// Send periodic heartbeat frames to keep WebSocket connections healthy through intermediaries.
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)

	// start another goroutine that sends a request every 10 sec to maintain the long-lived connection
	go func() {

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeatDone:
				return
			case <-ticker.C:
				hbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_ = wsjson.Write(hbCtx, conn, map[string]string{"action": "heartbeat"})
				cancel()
			}
		}
	}()

	approved := make(map[string]bool, len(symbols))

	// loop through real-time price push notification
	for {
		var event struct {
			Event     string               `json:"event"`
			Status    string               `json:"status"`
			Symbol    string               `json:"symbol"`
			Price     float64              `json:"price"`
			Timestamp int64                `json:"timestamp"`
			Success   []twelveSubscription `json:"success"`
		}

		if err := wsjson.Read(ctx, conn, &event); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("twelvedata price stream read failed (%T)", err)
		}

		if event.Event == "subscribe-status" {
			approved = p.approvedTwelveSymbols(symbols, event.Success)
			if len(approved) == 0 {
				return errTwelveSubscriptionsDenied
			}
		}

		if event.Event == "status" && event.Status == "error" {
			return errTwelveSubscriptionsDenied
		}
		if event.Event != "price" || !approved[event.Symbol] || event.Price <= 0 || event.Timestamp <= 0 {
			continue
		}

		tick := RawTick{
			Symbol:    event.Symbol,
			Price:     event.Price,
			Volume:    0,
			Timestamp: time.Unix(event.Timestamp, 0).UTC(),
		}

		select {
		case out <- tick:
		case <-ctx.Done():
			return nil
		}
	}
}

type twelveSubscription struct {
	Symbol string `json:"symbol"`
}

/** approvedTwelveSymbols records each requested symbol's subscription outcome. */
func (p *TwelveDataProvider) approvedTwelveSymbols(symbols []string, success []twelveSubscription) map[string]bool {
	approved := make(map[string]bool, len(success))
	for _, symbol := range symbols {
		for _, result := range success {
			if result.Symbol == symbol {
				approved[symbol] = true
				break
			}
		}
		p.logger.Info("twelvedata price subscription", "symbol", symbol, "approved", approved[symbol])
	}
	return approved
}

func twelveDataInterval(tf string) (string, bool) {
	switch tf {
	case "1m":
		return "1min", true
	case "5m":
		return "5min", true
	case "1h":
		return "1h", true
	case "1d":
		return "1day", true
	case "1w":
		return "1week", true
	case "1M":
		return "1month", true
	default:
		return "", false
	}
}

func parseTwelveDataTime(raw, tf string) (int64, error) {
	if (tf == "1d" || tf == "1w" || tf == "1M") && len(raw) == 10 {
		t, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
		if err != nil {
			return 0, err
		}
		if aligned, ok := AlignOpenTime(t, tf); ok {
			return aligned, nil
		}
		return t.Unix(), nil
	}

	t, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.UTC)
	if err != nil {
		return 0, err
	}

	return t.Unix(), nil
}

// FinnhubProvider adapts Finnhub REST candle queries and real-time trade WebSocket feeds.
type FinnhubProvider struct {
	apiKey string
	http   *http.Client
	logger *slog.Logger
}

/** NewFinnhubProvider constructs a Finnhub market data provider. */
func NewFinnhubProvider(apiKey string, logger *slog.Logger) *FinnhubProvider {
	return &FinnhubProvider{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 15 * time.Second},
		logger: logger,
	}
}

/** Name returns "finnhub". */
func (p *FinnhubProvider) Name() string {
	return "finnhub"
}

/** Mode returns "live". */
func (p *FinnhubProvider) Mode() string {
	return "live"
}

/** FetchHistory retrieves historical candles via Finnhub REST candle endpoints. */
func (p *FinnhubProvider) FetchHistory(ctx context.Context, symbol, tf string, limit int) ([]Candle, error) {
	fhSymbol, isForex := mapToFinnhubSymbol(symbol)
	res, ok := finnhubResolution(tf)
	if !ok {
		return nil, fmt.Errorf("unsupported timeframe: %s", tf)
	}

	secPerBar, _ := TimeframeSeconds(tf)
	if limit <= 0 || limit > MaxCandleLimit {
		limit = DefaultCandleLimit
	}

	toSec := time.Now().UTC().Unix()
	fromSec := toSec - int64(limit)*secPerBar*2
	pathSegment := "stock/candle"
	if isForex {
		pathSegment = "forex/candle"
	}

	query := url.Values{
		"symbol":     {fhSymbol},
		"resolution": {res},
		"from":       {strconv.FormatInt(fromSec, 10)},
		"to":         {strconv.FormatInt(toSec, 10)},
		"token":      {p.apiKey},
	}

	endpoint := "https://finnhub.io/api/v1/" + pathSegment + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("finnhub history request failed (%T)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("finnhub history status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Status string    `json:"s"`
		Open   []float64 `json:"o"`
		High   []float64 `json:"h"`
		Low    []float64 `json:"l"`
		Close  []float64 `json:"c"`
		Volume []float64 `json:"v"`
		Times  []int64   `json:"t"`
	}

	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	if parsed.Status != "ok" {
		if parsed.Status == "no_data" {
			return []Candle{}, nil
		}
		return nil, errors.New("finnhub candle payload rejected")
	}

	count := len(parsed.Times)
	if count > limit {
		startIdx := count - limit
		parsed.Times = parsed.Times[startIdx:]
		parsed.Open = parsed.Open[startIdx:]
		parsed.High = parsed.High[startIdx:]
		parsed.Low = parsed.Low[startIdx:]
		parsed.Close = parsed.Close[startIdx:]
		if len(parsed.Volume) == count {
			parsed.Volume = parsed.Volume[startIdx:]
		}
	}

	out := make([]Candle, 0, len(parsed.Times))
	for i := range parsed.Times {
		var vol float64
		if i < len(parsed.Volume) {
			vol = parsed.Volume[i]
		}

		ts := parsed.Times[i]
		if aligned, ok := AlignOpenTime(time.Unix(ts, 0).UTC(), tf); ok {
			ts = aligned
		}

		out = append(out, Candle{
			Time:   ts,
			Open:   parsed.Open[i],
			High:   parsed.High[i],
			Low:    parsed.Low[i],
			Close:  parsed.Close[i],
			Volume: vol,
		})
	}

	return out, nil
}

/** Stream opens a Finnhub WebSocket connection and decodes trade bursts into raw ticks. */
func (p *FinnhubProvider) Stream(ctx context.Context, symbols []string, out chan<- RawTick) error {
	endpoint := "wss://ws.finnhub.io?token=" + url.QueryEscape(p.apiKey)
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("finnhub dial failed (%T)", err)
	}
	defer conn.CloseNow()

	reverseMap := make(map[string]string, len(symbols))
	for _, sym := range symbols {
		fhSym, _ := mapToFinnhubSymbol(sym)
		reverseMap[fhSym] = sym

		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := wsjson.Write(writeCtx, conn, map[string]string{
			"type":   "subscribe",
			"symbol": fhSym,
		})
		cancel()
		if err != nil {
			return err
		}
	}

	for {
		var msg struct {
			Type string `json:"type"`
			Data []struct {
				Symbol    string  `json:"s"`
				Price     float64 `json:"p"`
				Volume    float64 `json:"v"`
				Timestamp int64   `json:"t"`
			} `json:"data"`
		}

		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		if msg.Type != "trade" {
			continue
		}

		for _, tr := range msg.Data {
			internalSym, mapped := reverseMap[tr.Symbol]
			if !mapped || tr.Price <= 0 {
				continue
			}

			ts := time.UnixMilli(tr.Timestamp).UTC()
			if tr.Timestamp <= 0 {
				ts = time.Now().UTC()
			}

			tick := RawTick{
				Symbol:    internalSym,
				Price:     tr.Price,
				Volume:    tr.Volume,
				Timestamp: ts,
			}

			select {
			case out <- tick:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func mapToFinnhubSymbol(symbol string) (string, bool) {
	if symbol == "XAU/USD" {
		return "OANDA:XAU_USD", true
	}
	return symbol, false
}

func finnhubResolution(tf string) (string, bool) {
	switch tf {
	case "1m":
		return "1", true
	case "5m":
		return "5", true
	case "1h":
		return "60", true
	case "1d":
		return "D", true
	case "1w":
		return "W", true
	case "1M":
		return "M", true
	default:
		return "", false
	}
}
