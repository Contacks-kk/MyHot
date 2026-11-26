package market

import (
	"context"
	"strings"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

// Repository encapsulates PostgreSQL persistence for archived market candles and personal news markers.
type Repository struct {
	db     *gorm.DB
	source string
}

/** NewRepository constructs a PostgreSQL-backed market repository. */
func NewRepository(db *gorm.DB, source string) *Repository {
	return &Repository{
		db:     db,
		source: source,
	}
}

/** UpsertCandles persists finalized or historical OHLCV bars in bounded batches. */
func (r *Repository) UpsertCandles(ctx context.Context, symbol, tf string, candles []Candle) error {
	if r == nil || r.db == nil || len(candles) == 0 || !IsPersistedTimeframe(tf) {
		return nil
	}

	symbol = r.source + ":" + symbol
	const batchSize = 200

	for start := 0; start < len(candles); start += batchSize {
		end := min(start+batchSize, len(candles))
		batch := candles[start:end]

		placeholders := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*8)

		for _, bar := range batch {
			placeholders = append(placeholders, "(?, ?, to_timestamp(?), ?, ?, ?, ?, ?, now())")
			args = append(args,
				symbol,
				tf,
				bar.Time,
				bar.Open,
				bar.High,
				bar.Low,
				bar.Close,
				bar.Volume,
			)
		}

		// Update OHLCV idempotently on conflict with the composite primary key.
		query := `INSERT INTO market_candles (symbol, timeframe, open_time, open, high, low, close, volume, updated_at)
			VALUES ` + strings.Join(placeholders, ",") + `
			ON CONFLICT (symbol, timeframe, open_time) DO UPDATE SET
				open = excluded.open,
				high = excluded.high,
				low = excluded.low,
				close = excluded.close,
				volume = excluded.volume,
				updated_at = now()`

		if err := r.db.WithContext(ctx).Exec(query, args...).Error; err != nil {
			return err
		}
	}

	return nil
}

/** ListCandles returns up to limit archived bars ordered ascending by UTC timestamp. */
func (r *Repository) ListCandles(ctx context.Context, symbol, tf string, limit int) ([]Candle, error) {
	if r == nil || r.db == nil {
		return []Candle{}, nil
	}

	if limit <= 0 || limit > MaxCandleLimit {
		limit = DefaultCandleLimit
	}

	symbol = r.source + ":" + symbol
	var rows []struct {
		OpenTime int64   `gorm:"column:open_time_sec"`
		Open     float64 `gorm:"column:open"`
		High     float64 `gorm:"column:high"`
		Low      float64 `gorm:"column:low"`
		Close    float64 `gorm:"column:close"`
		Volume   float64 `gorm:"column:volume"`
	}

	var err error
	if tf == "1w" || tf == "1M" {
		truncUnit := "week"
		if tf == "1M" {
			truncUnit = "month"
		}

		// Roll up persisted daily candles in PostgreSQL when Redis weekly/monthly cache is cold.
		err = r.db.WithContext(ctx).Raw(`
			SELECT extract(epoch FROM (date_trunc(?, open_time AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'))::bigint AS open_time_sec,
			       (array_agg(open ORDER BY open_time ASC))[1] AS open,
			       max(high) AS high,
			       min(low) AS low,
			       (array_agg(close ORDER BY open_time DESC))[1] AS close,
			       sum(volume) AS volume
			FROM market_candles
			WHERE symbol = ? AND timeframe = '1d'
			GROUP BY 1
			ORDER BY 1 DESC
			LIMIT ?`,
			truncUnit, symbol, limit,
		).Scan(&rows).Error
	} else {
		err = r.db.WithContext(ctx).Raw(`
			SELECT extract(epoch FROM open_time)::bigint AS open_time_sec,
			       open, high, low, close, volume
			FROM market_candles
			WHERE symbol = ? AND timeframe = ?
			ORDER BY open_time DESC
			LIMIT ?`,
			symbol, tf, limit,
		).Scan(&rows).Error
	}
	if err != nil {
		return nil, err
	}

	out := make([]Candle, len(rows))
	for i := range rows {
		src := rows[len(rows)-1-i]
		out[i] = Candle{
			Time:   src.OpenTime,
			Open:   src.Open,
			High:   src.High,
			Low:    src.Low,
			Close:  src.Close,
			Volume: src.Volume,
		}
	}

	return out, nil
}

/** PruneMinuteCandles deletes aged 1m bars beyond the retention horizon. */
func (r *Repository) PruneMinuteCandles(ctx context.Context, retentionDays int) error {
	if r == nil || r.db == nil || retentionDays <= 0 {
		return nil
	}

	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	return r.db.WithContext(ctx).Exec(
		`DELETE FROM market_candles WHERE symbol LIKE ? AND timeframe = '1m' AND open_time < ?`,
		r.source+":%", cutoff,
	).Error
}

/** QueryPersonalMarkers fetches high-score user articles relevant to a market asset within a time window. */
func (r *Repository) QueryPersonalMarkers(ctx context.Context, userID int64, meta SymbolMeta, fromSec, toSec int64, limit int) ([]NewsMarker, error) {
	if r == nil || r.db == nil || userID <= 0 || len(meta.Keywords) == 0 {
		return []NewsMarker{}, nil
	}

	if limit <= 0 {
		limit = 20
	}

	patterns := make([]string, 0, len(meta.Keywords))
	for _, kw := range meta.Keywords {
		trimmed := strings.TrimSpace(kw)
		if trimmed != "" {
			patterns = append(patterns, "%"+trimmed+"%")
		}
	}

	if len(patterns) == 0 {
		return []NewsMarker{}, nil
	}

	var rows []struct {
		ArticleID  string  `gorm:"column:article_id"`
		Title      string  `gorm:"column:title"`
		Score      float64 `gorm:"column:score"`
		Source     string  `gorm:"column:source"`
		EventEpoch int64   `gorm:"column:event_epoch"`
	}

	// Match private user analysis results whose title or summary references tracked asset keywords.
	err := r.db.WithContext(ctx).Raw(`
		SELECT r.article_id,
		       r.title,
		       r.relevance_score::float8 AS score,
		       coalesce(sub.name, s.name) AS source,
		       extract(epoch FROM coalesce(a.published_at, a.discovered_at))::bigint AS event_epoch
		FROM user_article_results r
		JOIN articles a ON a.id = r.article_id
		JOIN sources s ON s.id = a.source_id
		LEFT JOIN user_source_subscriptions sub ON sub.user_id = r.user_id AND sub.source_id = s.id
		WHERE r.user_id = ?
		  AND r.relevance_score >= 65
		  AND extract(epoch FROM coalesce(a.published_at, a.discovered_at)) BETWEEN ? AND ?
		  AND (r.title ILIKE ANY(?::text[]) OR r.summary ILIKE ANY(?::text[]))
		ORDER BY r.relevance_score DESC, event_epoch DESC
		LIMIT ?`,
		userID, fromSec, toSec, model.TextArray(patterns), model.TextArray(patterns), limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	markers := make([]NewsMarker, 0, len(rows))
	for _, row := range rows {
		markers = append(markers, NewsMarker{
			Time:   row.EventEpoch,
			ID:     row.ArticleID,
			Title:  row.Title,
			Score:  row.Score,
			Source: row.Source,
			Scope:  "personal",
			Href:   "/my/articles/" + row.ArticleID,
		})
	}

	return markers, nil
}
