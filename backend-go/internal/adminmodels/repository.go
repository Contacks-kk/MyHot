package adminmodels

import (
	"context"
	"encoding/json"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

type UsageRow struct {
	Purpose           string   `gorm:"column:purpose"`
	Model             *string  `gorm:"column:model"`
	PromptVersion     *string  `gorm:"column:prompt_version"`
	Calls             int      `gorm:"column:calls"`
	OK                int      `gorm:"column:ok"`
	Failed            int      `gorm:"column:failed"`
	Unknown           int      `gorm:"column:unknown"`
	P50               *int64   `gorm:"column:p50"`
	P95               *int64   `gorm:"column:p95"`
	TokensIn          *int64   `gorm:"column:tokens_in"`
	TokensOut         *int64   `gorm:"column:tokens_out"`
	TokensCached      *int64   `gorm:"column:tokens_cached"`
	TokensInUncosted  *int64   `gorm:"column:tokens_in_uncosted"`
	TokensOutUncosted *int64   `gorm:"column:tokens_out_uncosted"`
	ActualCost        *float64 `gorm:"column:actual_cost"`
	Currency          *string  `gorm:"column:currency"`
}

type Price struct {
	Service       string   `gorm:"column:service"`
	Model         string   `gorm:"column:model"`
	Currency      string   `gorm:"column:currency"`
	InputPerMTok  *float64 `gorm:"column:input_per_mtok"`
	CachedPerMTok *float64 `gorm:"column:cached_per_mtok"`
	OutputPerMTok *float64 `gorm:"column:output_per_mtok"`
}

type OverviewRows struct {
	Usage   []UsageRow
	Prices  []Price
	History []HistoryRow
	Benches []Bench
}

type HistoryRow struct {
	At      time.Time       `gorm:"column:at"`
	Actor   string          `gorm:"column:actor"`
	Subject string          `gorm:"column:subject"`
	Reason  *string         `gorm:"column:reason"`
	Before  json.RawMessage `gorm:"column:before;type:jsonb"`
	After   json.RawMessage `gorm:"column:after;type:jsonb"`
}

func (r *GormRepository) Overview(ctx context.Context, since time.Time) (OverviewRows, error) {
	var out OverviewRows
	err := r.db.WithContext(ctx).Raw(`
		SELECT r.purpose, a.model, r.request->>'promptVersion' AS prompt_version, count(*)::int AS calls,
		       count(*) FILTER (WHERE a.status = 'received')::int AS ok,
		       count(*) FILTER (WHERE a.status = 'failed')::int AS failed,
		       count(*) FILTER (WHERE a.status = 'unknown')::int AS unknown,
	       percentile_disc(0.5) WITHIN GROUP (ORDER BY a.latency_ms) AS p50,
	       percentile_disc(0.95) WITHIN GROUP (ORDER BY a.latency_ms) AS p95,
	       sum((a.usage->>'prompt_tokens')::bigint) AS tokens_in, sum((a.usage->>'completion_tokens')::bigint) AS tokens_out,
	       sum((a.usage->>'prompt_tokens')::bigint) FILTER (WHERE a.cost IS NULL) AS tokens_in_uncosted,
	       sum((a.usage->>'completion_tokens')::bigint) FILTER (WHERE a.cost IS NULL) AS tokens_out_uncosted,
	       sum(coalesce((a.usage->'prompt_tokens_details'->>'cached_tokens')::bigint, (a.usage->>'prompt_cache_hit_tokens')::bigint, 0)) FILTER (WHERE a.cost IS NULL) AS tokens_cached,
	       sum(a.cost) FILTER (WHERE a.cost_basis = 'actual') AS actual_cost, max(a.currency) AS currency
		FROM receipt_attempts a JOIN receipts r ON r.id = a.receipt_id
		WHERE a.started_at >= ? AND a.origin = 'live' AND a.model IS NOT NULL
		GROUP BY 1, 2, 3 ORDER BY 1, 4 DESC`, since).Scan(&out.Usage).Error
	if err != nil {
		return OverviewRows{}, err
	}
	if err := r.db.WithContext(ctx).Table("service_prices").Select("service, model, currency, input_per_mtok, cached_per_mtok, output_per_mtok").Scan(&out.Prices).Error; err != nil {
		return OverviewRows{}, err
	}
	err = r.db.WithContext(ctx).Model(&model.AdminAuditLog{}).Select("created_at AS at, actor, subject, reason, before, after").
		Where("action = ?", "models.switch").Order("created_at DESC").Limit(30).Scan(&out.History).Error
	if err != nil {
		return OverviewRows{}, err
	}
	err = r.db.WithContext(ctx).Raw(`SELECT id, label, sample_size, prompt_version, models,
		(SELECT coalesce(jsonb_object_agg(key, value - 'sweep'), '{}'::jsonb) FROM jsonb_each(r.summary)) AS summary,
		created_at FROM selectbench_runs r ORDER BY created_at DESC LIMIT 8`).Scan(&out.Benches).Error
	return out, err
}

func (r *GormRepository) Overrides(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		Key   string          `gorm:"column:key"`
		Value json.RawMessage `gorm:"column:value"`
	}
	if err := r.db.WithContext(ctx).Table("settings").Select("key,value").Where("key LIKE ?", "models.%").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		var value struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(row.Value, &value) == nil && value.Model != "" {
			out[row.Key[len("models."):]] = value.Model
		}
	}
	return out, nil
}

func (r *GormRepository) Switch(ctx context.Context, capability string, modelKey *string, actor, reason string, before, after ModelSource) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return err
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		key := "models." + capability
		if modelKey == nil {
			if err := tx.Exec("DELETE FROM settings WHERE key = ?", key).Error; err != nil {
				return err
			}
		} else {
			value, _ := json.Marshal(map[string]string{"model": *modelKey})
			if err := tx.Exec(`INSERT INTO settings (key,value,updated_by) VALUES (?,?::jsonb,?)
				ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,updated_by=EXCLUDED.updated_by,updated_at=now()`, key, string(value), actor).Error; err != nil {
				return err
			}
		}
		subject := "capability:" + capability
		return tx.Exec(`INSERT INTO audit_log (actor,action,subject,reason,before,after)
			VALUES (?,'models.switch',?,?,?::jsonb,?::jsonb)`, actor, subject, reason, string(beforeJSON), string(afterJSON)).Error
	})
}
