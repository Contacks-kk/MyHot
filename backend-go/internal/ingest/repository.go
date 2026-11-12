package ingest

import (
	"context"
	"encoding/json"
	"errors"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) EnsureSource(ctx context.Context, id, name string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	result := r.db.WithContext(ctx).Raw(`INSERT INTO sources (id, name, kind, config, tier, participation_mode, interval_minutes, enabled, health, tags)
		VALUES (?, ?, 'external', '{}'::jsonb, 'T2', 'isolated', 1440, true, 'ok', ARRAY['ingest:auto-created']::text[])
		ON CONFLICT (id) DO UPDATE SET last_ok_at = now() WHERE sources.enabled
		RETURNING id`, id, name).Scan(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSourcePaused
	}
	return nil
}

func (r *GormRepository) RecordSuccess(ctx context.Context, sourceID string, received, created int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("sources").Where("id = ?", sourceID).Updates(map[string]any{"last_fetch_at": gorm.Expr("now()"), "last_ok_at": gorm.Expr("now()")}).Error; err != nil {
			return err
		}
		summary, err := json.Marshal(map[string]any{"sourceId": sourceID, "received": received, "created": created})
		if err != nil {
			return err
		}
		return tx.Create(&model.IngestEvent{Client: "ingest-items", Kind: "items", Status: "ok", Summary: summary}).Error
	})
}

func IsPaused(err error) bool { return errors.Is(err, ErrSourcePaused) }
