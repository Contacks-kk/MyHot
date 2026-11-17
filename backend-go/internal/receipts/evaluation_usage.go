package receipts

import (
	"context"
	"math"
)

type EvaluationUsage struct {
	TokensIn     int64 `json:"tokensIn"`
	TokensOut    int64 `json:"tokensOut"`
	AvgLatencyMS int64 `json:"avgLatencyMs"`
}

/** EvaluationUsage totals all recorded attempts for the selected receipts. */
func (r *GormRepository) EvaluationUsage(ctx context.Context, receiptIDs []int64) (EvaluationUsage, error) {
	if len(receiptIDs) == 0 {
		return EvaluationUsage{}, nil
	}
	var row struct {
		TokensIn   int64   `gorm:"column:tokens_in"`
		TokensOut  int64   `gorm:"column:tokens_out"`
		AvgLatency float64 `gorm:"column:avg_latency"`
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(COALESCE((usage->>'prompt_tokens')::bigint, (usage->>'input_tokens')::bigint, 0)), 0) AS tokens_in,
		       COALESCE(SUM(COALESCE((usage->>'completion_tokens')::bigint, (usage->>'output_tokens')::bigint, 0)), 0) AS tokens_out,
		       COALESCE(AVG(latency_ms), 0) AS avg_latency
		FROM receipt_attempts WHERE receipt_id IN ?`, receiptIDs).Scan(&row).Error
	if err != nil {
		return EvaluationUsage{}, err
	}
	return EvaluationUsage{
		TokensIn:     row.TokensIn,
		TokensOut:    row.TokensOut,
		AvgLatencyMS: int64(math.Round(row.AvgLatency)),
	}, nil
}
