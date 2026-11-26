package market

import (
	"context"
	"sort"
	"strings"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

// Service coordinates market catalog, hot/cold candle merging, and news markers for HTTP and WebSocket handlers.
type Service struct {
	repo            *Repository
	redis           *RedisStore
	publicContent   *publiccontent.Service
	enabled         bool
	providerName    string
	providerMode    string
	imgProxySecret  string
	xPostIsFullText bool
}

// ServiceConfig holds runtime flags controlling market feature visibility and content links.
type ServiceConfig struct {
	Enabled         bool
	ProviderName    string
	ProviderMode    string
	ImgProxySecret  string
	XPostIsFullText bool
}

/** NewService creates a market application service. */
func NewService(repo *Repository, redis *RedisStore, pub *publiccontent.Service, cfg ServiceConfig) *Service {
	mode := cfg.ProviderMode
	if !cfg.Enabled && mode != "unavailable" {
		mode = "disabled"
	}

	return &Service{
		repo:            repo,
		redis:           redis,
		publicContent:   pub,
		enabled:         cfg.Enabled,
		providerName:    cfg.ProviderName,
		providerMode:    mode,
		imgProxySecret:  cfg.ImgProxySecret,
		xPostIsFullText: cfg.XPostIsFullText,
	}
}

/** SetRedis attaches a configured RedisStore to the running market service. */
func (s *Service) SetRedis(redis *RedisStore) {
	if s == nil {
		return
	}

	s.redis = redis
}

/** Catalog returns supported symbols, timeframes, runtime flags, and latest quotes. */
func (s *Service) Catalog(ctx context.Context) (MarketsCatalogResponse, error) {
	return MarketsCatalogResponse{
		Enabled:    s.enabled,
		Provider:   s.providerName,
		Mode:       s.providerMode,
		Timeframes: SupportedTimeframes(),
		Symbols:    Catalog(),
		Quotes:     s.quotes(ctx),
	}, nil
}

/** quotes loads only the selected provider's snapshots, using stored closes if Redis is cold. */
func (s *Service) quotes(ctx context.Context) map[string]QuoteSnapshot {
	if !s.enabled {
		return map[string]QuoteSnapshot{}
	}
	quotes, err := s.redis.GetAllQuotes(ctx)
	if err != nil {
		quotes = make(map[string]QuoteSnapshot)
	}

	// Fill missing quotes from latest PostgreSQL daily/minute closes when Redis cache is cold.
	for _, meta := range supportedSymbols {
		if quote, exists := quotes[meta.Symbol]; exists {
			if s.providerName == "twelvedata" && quote.Mode == "live" && time.Since(time.Unix(quote.UpdatedAt, 0)) > 2*time.Minute {
				quote.Mode = "rest"
				quotes[meta.Symbol] = quote
			}
			continue
		}

		if s.providerName == "twelvedata" {
			bars, err := s.repo.ListCandles(ctx, meta.Symbol, "1m", 1)
			if err == nil && len(bars) > 0 {
				bar := bars[0]
				quotes[meta.Symbol] = QuoteSnapshot{
					Symbol:    meta.Symbol,
					Price:     bar.Close,
					UpdatedAt: bar.Time + 60,
					Mode:      "rest",
				}
			}
			continue
		}

		latest, listErr := s.repo.ListCandles(ctx, meta.Symbol, "1d", 2)
		if listErr != nil || len(latest) == 0 {
			latest, listErr = s.repo.ListCandles(ctx, meta.Symbol, "1m", 2)
		}

		if listErr == nil && len(latest) > 0 {
			cur := latest[len(latest)-1]
			prevClose := cur.Open
			if len(latest) >= 2 {
				prevClose = latest[len(latest)-2].Close
			}

			quotes[meta.Symbol] = buildQuote(
				meta.Symbol,
				cur.Close,
				cur.Open,
				prevClose,
				cur.High,
				cur.Low,
				cur.Volume,
				time.Unix(cur.Time, 0).UTC(),
				s.providerMode,
			)
		}
	}

	return quotes
}

/** Candles loads the merged historical + active candle window alongside relevant personal and public news markers. */
func (s *Service) Candles(ctx context.Context, userID int64, symbol, tf string, limit int) (CandlesResponse, error) {
	meta, ok := LookupSymbol(symbol)
	if !ok {
		symbol = supportedSymbols[0].Symbol
		meta = supportedSymbols[0]
	}

	if !IsValidTimeframe(tf) {
		tf = "1m"
	}

	if limit <= 0 || limit > MaxCandleLimit {
		limit = DefaultCandleLimit
	}
	if !s.enabled {
		return CandlesResponse{
			Symbol:    symbol,
			Timeframe: tf,
			Mode:      s.providerMode,
			Candles:   []Candle{},
			Markers:   []NewsMarker{},
		}, nil
	}

	// Prefer hot Redis ZSET window; fall back transparently to PostgreSQL archive.
	candles, err := s.redis.GetWindowCandles(ctx, symbol, tf, limit)
	if err != nil || len(candles) == 0 || (s.providerName == "twelvedata" && len(candles) < limit) {
		stored, err := s.repo.ListCandles(ctx, symbol, tf, limit)
		if err != nil && len(candles) == 0 {
			return CandlesResponse{}, err
		}
		if len(candles) == 0 || len(stored) > len(candles) {
			candles = stored
		}
	}

	// Merge active unclosed bar so the latest candle continues seamlessly before the WebSocket stream takes over.
	live, liveErr := s.redis.GetLiveCandle(ctx, symbol, tf)
	if s.providerName == "twelvedata" && live != nil &&
		(tf != "1m" || live.Time != time.Now().UTC().Truncate(time.Minute).Unix() ||
			(len(candles) > 0 && live.Time <= candles[len(candles)-1].Time)) {
		live = nil
	}
	if liveErr == nil && live != nil {
		if len(candles) == 0 {
			candles = append(candles, *live)
		} else if candles[len(candles)-1].Time == live.Time {
			candles[len(candles)-1] = *live
		} else if candles[len(candles)-1].Time < live.Time {
			candles = append(candles, *live)
			if len(candles) > limit {
				candles = candles[len(candles)-limit:]
			}
		}
	}

	var quotePtr *QuoteSnapshot
	if quote, exists := s.quotes(ctx)[symbol]; exists {
		quotePtr = &quote
	}

	markers := s.collectMarkers(ctx, userID, meta, candles, tf)

	return CandlesResponse{
		Symbol:    symbol,
		Timeframe: tf,
		Mode:      s.providerMode,
		Quote:     quotePtr,
		Candles:   candles,
		Markers:   markers,
	}, nil
}

func (s *Service) collectMarkers(ctx context.Context, userID int64, meta SymbolMeta, candles []Candle, tf string) []NewsMarker {
	if len(candles) == 0 {
		return []NewsMarker{}
	}

	fromSec := candles[0].Time
	toSec := NextOpenTime(candles[len(candles)-1].Time, tf)

	seen := make(map[string]struct{})
	merged := make([]NewsMarker, 0, 24)

	personalMarkers, err := s.repo.QueryPersonalMarkers(ctx, userID, meta, fromSec, toSec, 15)
	if err == nil {
		for _, m := range personalMarkers {
			if aligned, valid := AlignOpenTime(time.Unix(m.Time, 0).UTC(), tf); valid {
				m.Time = aligned
			}

			seen[m.ID] = struct{}{}
			merged = append(merged, m)
		}
	}

	// Supplement from publiccontent service following shared publication eligibility boundaries.
	if s.publicContent != nil && len(meta.Keywords) > 0 {
		searchTerm := meta.Keywords[0]
		poolResp, poolErr := s.publicContent.SitePool(
			ctx,
			publiccontent.SitePoolQuery{
				Channel: "all",
				Search:  &searchTerm,
				Tab:     "time",
				Page:    1,
			},
			time.Now().UTC(),
			s.imgProxySecret,
			s.xPostIsFullText,
		)
		if poolErr == nil {
			for _, item := range poolResp.Items {
				if _, exists := seen[item.ID]; exists {
					continue
				}

				if item.Score == nil || *item.Score < 70 {
					continue
				}

				rawTime := item.TimelineAt
				if item.PublishedAt != nil && *item.PublishedAt != "" {
					rawTime = *item.PublishedAt
				}

				parsedTime, tErr := time.Parse(time.RFC3339, rawTime)
				if tErr != nil {
					continue
				}

				epoch := parsedTime.UTC().Unix()
				if epoch < fromSec || epoch > toSec {
					continue
				}

				if !matchesAnyKeyword(item.Title, meta.Keywords) {
					continue
				}

				aligned, _ := AlignOpenTime(parsedTime.UTC(), tf)
				seen[item.ID] = struct{}{}
				merged = append(merged, NewsMarker{
					Time:   aligned,
					ID:     item.ID,
					Title:  item.Title,
					Score:  float64(*item.Score),
					Source: item.Source.Name,
					Scope:  "public",
					Href:   "/items/" + item.ID,
				})

				if len(merged) >= 24 {
					break
				}
			}
		}
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Time == merged[j].Time {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].Time < merged[j].Time
	})

	if len(merged) > 20 {
		merged = merged[len(merged)-20:]
	}

	return merged
}

func matchesAnyKeyword(text string, keywords []string) bool {
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}
