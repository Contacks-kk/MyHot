package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

/** Reuse returns a receipt result without changing receipt or budget state. */
func (r *GormRepository) Reuse(ctx context.Context, logicalKey string) (Claim, error) {
	var receipt model.PaidReceipt
	result := r.db.WithContext(ctx).Select("id", "status", "response", "updated_at").Where("logical_key = ?", logicalKey).Take(&receipt)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return Claim{}, nil
	}
	if result.Error != nil {
		return Claim{}, result.Error
	}

	switch receipt.Status {
	case "received", "completed":
		return Claim{
			ReceiptID: receipt.ID,
			Response:  receipt.Response,
			Reused:    true,
		}, nil
	case "pending":
		if time.Since(receipt.UpdatedAt) >= PendingStaleAfter {
			return Claim{
				ReceiptID: receipt.ID,
				Unknown:   true,
			}, nil
		}
		return Claim{
			ReceiptID: receipt.ID,
			Busy:      true,
		}, nil
	case "unknown":
		return Claim{
			ReceiptID: receipt.ID,
			Unknown:   true,
		}, nil
	case "failed":
		return Claim{}, nil
	default:
		return Claim{}, errors.New("paid receipt has an unsupported status")
	}
}

func (r *GormRepository) Claim(ctx context.Context, request Request, logicalKey string) (Claim, error) {
	var claim Claim
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "budget:"+request.Service).Error; err != nil {
			return err
		}

		var receipt model.PaidReceipt
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("logical_key = ?", logicalKey).Take(&receipt)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if result.RowsAffected > 0 {
			switch receipt.Status {
			case "received", "completed":
				claim = Claim{ReceiptID: receipt.ID, Response: receipt.Response, Reused: true}
				return nil
			case "unknown":
				claim = Claim{ReceiptID: receipt.ID, Unknown: true}
				return nil
			case "pending":
				if time.Since(receipt.UpdatedAt) < PendingStaleAfter {
					claim = Claim{ReceiptID: receipt.ID, Busy: true}
					return nil
				}
				reason := "placeholder went stale without a recorded result"
				if err := tx.Model(&model.PaidReceipt{}).Where("id = ?", receipt.ID).Updates(map[string]any{
					"status": "unknown", "error": reason, "updated_at": gorm.Expr("now()"),
				}).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.PaidReceiptAttempt{}).Where("receipt_id = ? AND status = 'pending'", receipt.ID).Updates(map[string]any{
					"status": "unknown", "error": reason, "finished_at": gorm.Expr("now()"),
				}).Error; err != nil {
					return err
				}
				claim = Claim{ReceiptID: receipt.ID, Unknown: true}
				return nil
			case "failed":
				if err := r.checkBudget(tx, request.Service); err != nil {
					return err
				}
				attempt := receipt.Attempts + 1
				if err := tx.Model(&model.PaidReceipt{}).Where("id = ?", receipt.ID).Updates(map[string]any{
					"status": "pending", "attempts": attempt, "error": nil, "updated_at": gorm.Expr("now()"),
				}).Error; err != nil {
					return err
				}
				attemptID, err := createAttempt(tx, receipt.ID, attempt, request)
				if err != nil {
					return err
				}
				claim = Claim{ReceiptID: receipt.ID, AttemptID: attemptID}
				return nil
			default:
				return errors.New("paid receipt has an unsupported status")
			}
		}

		if err := r.checkBudget(tx, request.Service); err != nil {
			return err
		}
		summary, err := json.Marshal(request.Summary)
		if err != nil {
			return err
		}
		if request.Summary == nil {
			summary = []byte(`{}`)
		}
		modelName := optional(request.Model)
		subject := optional(request.Subject)
		created := model.PaidReceipt{
			LogicalKey: logicalKey, Service: request.Service, Model: modelName, Purpose: request.Purpose,
			Subject: subject, Status: "pending", Request: summary, Attempts: 1, Origin: "live",
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		attemptID, err := createAttempt(tx, created.ID, 1, request)
		if err != nil {
			return err
		}
		claim = Claim{ReceiptID: created.ID, AttemptID: attemptID}
		return nil
	})
	return claim, err
}

func (r *GormRepository) checkBudget(tx *gorm.DB, service string) error {
	var budget model.ServiceBudget
	result := tx.Where("service = ?", service).Take(&budget)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	if result.Error != nil {
		return result.Error
	}
	var counts struct {
		Minute int `gorm:"column:minute"`
		Hour   int `gorm:"column:hour"`
		Day    int `gorm:"column:day"`
	}
	if err := tx.Raw(`SELECT
		count(*) FILTER (WHERE started_at > now() - interval '1 minute')::int AS minute,
		count(*) FILTER (WHERE started_at > now() - interval '1 hour')::int AS hour,
		count(*)::int AS day
		FROM receipt_attempts WHERE service = ? AND origin = 'live'
		AND started_at > now() - interval '1 day'`, service).Scan(&counts).Error; err != nil {
		return err
	}
	if budget.PerMinute <= 0 || budget.PerHour <= 0 || budget.PerDay <= 0 {
		return &BudgetExceededError{Service: service, Window: "stopped", RetryAfterSecond: 3600}
	}
	if counts.Minute >= budget.PerMinute {
		return &BudgetExceededError{Service: service, Window: "minute", RetryAfterSecond: 60}
	}
	if counts.Hour >= budget.PerHour {
		return &BudgetExceededError{Service: service, Window: "hour", RetryAfterSecond: 600}
	}
	if counts.Day >= budget.PerDay {
		return &BudgetExceededError{Service: service, Window: "day", RetryAfterSecond: 3600}
	}
	return nil
}

func (r *GormRepository) RecordFailure(ctx context.Context, claim Claim, status, message string, latency time.Duration) error {
	if status != "failed" && status != "unknown" {
		return errors.New("invalid paid receipt failure status")
	}
	message = truncate(message, 2000)
	latencyMS := durationMS(latency)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PaidReceipt{}).Where("id = ?", claim.ReceiptID).Updates(map[string]any{
			"status": status, "error": message, "updated_at": gorm.Expr("now()"),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.PaidReceiptAttempt{}).Where("id = ?", claim.AttemptID).Updates(map[string]any{
			"status": status, "error": message, "latency_ms": latencyMS, "finished_at": gorm.Expr("now()"),
		}).Error
	})
}

func (r *GormRepository) RecordReceived(ctx context.Context, claim Claim, outcome Outcome, latency time.Duration) error {
	latencyMS := durationMS(latency)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PaidReceipt{}).Where("id = ?", claim.ReceiptID).Updates(map[string]any{
			"status": "received", "response": outcome.Response, "request_id": optional(outcome.RequestID),
			"usage": optionalJSON(outcome.Usage), "cost": outcome.Cost, "currency": optional(outcome.Currency),
			"cost_basis": optional(outcome.CostBasis), "received_at": gorm.Expr("now()"), "updated_at": gorm.Expr("now()"),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.PaidReceiptAttempt{}).Where("id = ?", claim.AttemptID).Updates(map[string]any{
			"status": "received", "request_id": optional(outcome.RequestID), "usage": optionalJSON(outcome.Usage),
			"cost": outcome.Cost, "currency": optional(outcome.Currency), "cost_basis": optional(outcome.CostBasis),
			"latency_ms": latencyMS, "finished_at": gorm.Expr("now()"),
		}).Error
	})
}

func (r *GormRepository) Complete(ctx context.Context, receiptID int64) error {
	return r.db.WithContext(ctx).Model(&model.PaidReceipt{}).Where("id = ?", receiptID).Updates(map[string]any{
		"status": "completed", "completed_at": gorm.Expr("coalesce(completed_at, now())"), "updated_at": gorm.Expr("now()"),
	}).Error
}

func (r *GormRepository) Reject(ctx context.Context, receiptID int64, reason string) error {
	return r.db.WithContext(ctx).Model(&model.PaidReceipt{}).Where("id = ? AND status = 'received'", receiptID).Updates(map[string]any{
		"status": "failed", "error": truncate(reason, 2000), "updated_at": gorm.Expr("now()"),
	}).Error
}

func createAttempt(tx *gorm.DB, receiptID int64, number int, request Request) (int64, error) {
	attempt := model.PaidReceiptAttempt{
		ReceiptID: receiptID, Attempt: number, Service: request.Service, Model: optional(request.Model),
		Origin: "live", Status: "pending", StartedAt: time.Now().UTC(),
	}
	if err := tx.Create(&attempt).Error; err != nil {
		return 0, err
	}
	return attempt.ID, nil
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func durationMS(value time.Duration) int {
	ms := int(value / time.Millisecond)
	if ms < 0 {
		return 0
	}
	return ms
}
