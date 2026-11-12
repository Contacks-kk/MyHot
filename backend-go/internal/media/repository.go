package media

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

type ImageBudgetState struct {
	Window   string    `json:"window"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
	DayBytes int64     `json:"dayBytes"`
}

type Repository interface {
	RecordBudgetTrip(context.Context, ImageBudgetState) error
}

type SettingModel struct {
	Key       string          `gorm:"column:key;primaryKey"`
	Value     json.RawMessage `gorm:"column:value;type:jsonb"`
	UpdatedBy string          `gorm:"column:updated_by"`
	UpdatedAt time.Time       `gorm:"column:updated_at"`
}

func (SettingModel) TableName() string { return "settings" }

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) RecordBudgetTrip(ctx context.Context, state ImageBudgetState) error {
	value, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Exec(`
		INSERT INTO settings (key, value, updated_by) VALUES ('egress.imgproxy', ?::jsonb, 'api')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, string(value)).Error
}
