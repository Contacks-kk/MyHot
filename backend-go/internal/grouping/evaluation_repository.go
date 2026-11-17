package grouping

import (
	"context"

	"aihot/backend-go/internal/receipts"
)

type EvaluationUsage = receipts.EvaluationUsage

/** EvaluationUsage totals recorded attempts for the specified logical receipts. */
func (r *GormRepository) EvaluationUsage(ctx context.Context, receiptIDs []int64) (EvaluationUsage, error) {
	return receipts.NewRepository(r.db).EvaluationUsage(ctx, receiptIDs)
}
