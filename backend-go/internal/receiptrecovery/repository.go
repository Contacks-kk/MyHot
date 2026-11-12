package receiptrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) Status(ctx context.Context, id int64) (string, bool, error) {
	var receipt model.PaidReceipt
	result := r.db.WithContext(ctx).Select("status").First(&receipt, id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	return receipt.Status, result.RowsAffected > 0, result.Error
}

func (r *GormRepository) Release(ctx context.Context, id int64, errorText, actor, note string, billed *bool) (*Result, bool, error) {
	var response *Result
	found := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var receipt model.PaidReceipt
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&receipt)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if query.Error != nil {
			return query.Error
		}
		found = true
		if receipt.Status != "unknown" {
			found = false
			return nil
		}
		changed := tx.Model(&model.PaidReceipt{}).Where("id = ? AND status = 'unknown'", id).Updates(map[string]any{
			"status": "failed", "error": errorText, "updated_at": gorm.Expr("now()"),
		})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected == 0 {
			found = false
			return nil
		}
		if err := tx.Model(&model.PaidReceiptAttempt{}).Where("receipt_id = ? AND status = 'unknown'", id).Updates(map[string]any{
			"status": "failed", "error": errorText,
		}).Error; err != nil {
			return err
		}

		requeued := resumeArticle(tx, receipt)
		if requeued.Error != nil {
			return requeued.Error
		}
		before, err := json.Marshal(map[string]any{"status": "unknown"})
		if err != nil {
			return err
		}
		after, err := json.Marshal(map[string]any{"status": "failed", "billed": billed, "requeued": requeued.RowsAffected > 0})
		if err != nil {
			return err
		}
		subject := fmt.Sprintf("receipt:%d", id)
		if err := tx.Create(&model.AdminAuditLog{Actor: actor, Action: "receipt.release", Subject: &subject, Reason: optionalString(note), Before: before, After: after}).Error; err != nil {
			return err
		}
		response = &Result{ID: id, Status: "failed", Subject: receipt.Subject, Purpose: receipt.Purpose, Requeued: requeued.RowsAffected > 0}
		return nil
	})
	return response, found, err
}

var analysisSubject = regexp.MustCompile(`^article:([^@:#]+)`)

func resumeArticle(tx *gorm.DB, receipt model.PaidReceipt) *gorm.DB {
	switch receipt.Purpose {
	case "embedding":
		if receipt.Subject == nil {
			return &gorm.DB{RowsAffected: 0}
		}
		match := analysisSubject.FindStringSubmatch(*receipt.Subject)
		if len(match) != 2 {
			return &gorm.DB{RowsAffected: 0}
		}
		return tx.Table("articles").Where("id = ? AND grouping_receipt_id = ? AND grouping_status = 'failed'", match[1], receipt.ID).Updates(map[string]any{
			"grouping_status": "pending", "grouping_receipt_id": nil, "grouping_error": nil, "grouped_at": nil,
		})
	case "prefilter_article", "score_article", "structure_article", "understand_article", "summarize_article":
	default:
		return &gorm.DB{RowsAffected: 0}
	}
	if receipt.Subject == nil {
		return &gorm.DB{RowsAffected: 0}
	}
	match := analysisSubject.FindStringSubmatch(*receipt.Subject)
	if len(match) != 2 {
		return &gorm.DB{RowsAffected: 0}
	}
	return tx.Model(&model.Article{}).Where("id = ? AND processing_state = 'failed'", match[1]).Updates(map[string]any{
		"processing_state": "new", "processing_attempts": 0, "processing_retry_at": nil, "processing_error": nil, "processing_queued_at": nil,
	})
}

func (r *GormRepository) MarkStalePending(ctx context.Context, cutoff time.Time) (int64, error) {
	const reason = "placeholder went stale without a recorded result"
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`UPDATE receipts SET status='unknown', error=?, updated_at=now()
			WHERE status='pending' AND updated_at < ? RETURNING id`, reason, cutoff).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]int64, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		return tx.Model(&model.PaidReceiptAttempt{}).Where("receipt_id IN ? AND status='pending'", ids).Updates(map[string]any{
			"status": "unknown", "error": reason, "finished_at": gorm.Expr("now()"),
		}).Error
	})
	return int64(len(rows)), err
}

func (r *GormRepository) DueUnknown(ctx context.Context, cutoff time.Time, limit int) ([]int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var ids []int64
	err := r.db.WithContext(ctx).Raw(`SELECT r.id FROM receipts r
		WHERE r.status='unknown' AND r.updated_at < ?
		AND NOT EXISTS (SELECT 1 FROM receipt_attempts a WHERE a.receipt_id=r.id AND a.error LIKE ?)
		ORDER BY r.id LIMIT ?`, cutoff, AutoReleaseNote+"%", limit).Scan(&ids).Error
	return ids, err
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

var _ Repository = (*GormRepository)(nil)
