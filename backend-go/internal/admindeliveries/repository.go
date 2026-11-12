package admindeliveries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

type deliveryVersion struct {
	Status  string `gorm:"column:status"`
	Version string `gorm:"column:version"`
}

func (r *GormRepository) Resolve(ctx context.Context, id int64, outcome, note, actor string) (*Result, bool, error) {
	var response *Result
	found := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before deliveryVersion
		query := tx.Model(&model.Delivery{}).Select("status, updated_at::text AS version").Where("id = ?", id).Take(&before)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if query.Error != nil {
			return query.Error
		}
		found = true
		if before.Status != "unknown" && before.Status != "failed" {
			return ErrNotResolvable
		}

		status := "failed"
		responseText := "人工放弃：" + note
		updates := map[string]any{"status": status, "response": responseText, "updated_at": gorm.Expr("now()")}
		if outcome == "sent" {
			status = "sent"
			responseText = "人工确认已送达：" + note
			updates["status"] = status
			updates["response"] = responseText
			updates["sent_at"] = gorm.Expr("coalesce(sent_at, now())")
		}
		changed := tx.Model(&model.Delivery{}).Where("id = ? AND status IN ? AND updated_at::text = ?", id, []string{"unknown", "failed"}, before.Version).Updates(updates)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected == 0 {
			return ErrChanged
		}

		beforeJSON, err := json.Marshal(map[string]string{"status": before.Status})
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(map[string]string{"status": status})
		if err != nil {
			return err
		}
		subject := fmt.Sprintf("delivery:%d", id)
		if err := tx.Create(&model.AdminAuditLog{
			Actor: actor, Action: "delivery." + outcome, Subject: &subject, Reason: &note,
			Before: beforeJSON, After: afterJSON,
		}).Error; err != nil {
			return err
		}
		response = &Result{ID: id, Status: status}
		return nil
	})
	return response, found, err
}

func (r *GormRepository) MarkStale(ctx context.Context, cutoff time.Time) (StaleCounts, error) {
	var counts StaleCounts
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		unknown := tx.Model(&model.Delivery{}).Where("status = ? AND updated_at < ?", "sending", cutoff).Updates(map[string]any{
			"status":     "unknown",
			"response":   gorm.Expr("coalesce(response, ?)", "\u53d1\u9001\u4e2d\u8fdb\u7a0b\u4e2d\u65ad\uff0c\u662f\u5426\u9001\u8fbe\u672a\u77e5"),
			"updated_at": gorm.Expr("now()"),
		})
		if unknown.Error != nil {
			return unknown.Error
		}
		failed := tx.Model(&model.Delivery{}).Where("status = ? AND updated_at < ?", "pending", cutoff).Updates(map[string]any{
			"status":     "failed",
			"response":   gorm.Expr("coalesce(response, ?)", "\u53d1\u9001\u524d\u8fdb\u7a0b\u4e2d\u65ad\uff0c\u6ca1\u6709\u53d1\u51fa"),
			"updated_at": gorm.Expr("now()"),
		})
		if failed.Error != nil {
			return failed.Error
		}
		counts = StaleCounts{Unknown: unknown.RowsAffected, Failed: failed.RowsAffected}
		return nil
	})
	return counts, err
}

var _ Repository = (*GormRepository)(nil)
