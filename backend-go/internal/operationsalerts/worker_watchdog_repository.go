package operationsalerts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/** Holds one database transaction and advisory lock across the watchdog transition. */
func (r *GormRepository) WithWorkerWatchdogLock(ctx context.Context, check func(WorkerWatchdogStateRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('watchdog.worker'))`).Error; err != nil {
			return err
		}
		return check(&gormWorkerWatchdogRepository{db: tx})
	})
}

type gormWorkerWatchdogRepository struct {
	db *gorm.DB
}

/** Reads the last database update time for the Worker heartbeat row. */
func (r *gormWorkerWatchdogRepository) WorkerHeartbeatUpdatedAt(ctx context.Context) (*time.Time, error) {
	var heartbeat struct {
		UpdatedAt *time.Time `gorm:"column:updated_at"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT updated_at FROM settings WHERE key = 'heartbeat.worker'`).Scan(&heartbeat).Error; err != nil {
		return nil, err
	}
	return heartbeat.UpdatedAt, nil
}

/** Loads the previously acknowledged Worker up/down state. */
func (r *gormWorkerWatchdogRepository) LoadWorkerWatchdogState(ctx context.Context) (*WorkerWatchdogState, error) {
	var setting model.AppSetting
	result := r.db.WithContext(ctx).Select("value").First(&setting, "key = ?", "watchdog.worker")
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	var state WorkerWatchdogState
	if err := json.Unmarshal(setting.Value, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

/** Persists the latest acknowledged Worker state in the existing settings table. */
func (r *gormWorkerWatchdogRepository) SaveWorkerWatchdogState(ctx context.Context, state WorkerWatchdogState) error {
	value, err := json.Marshal(state)
	if err != nil {
		return err
	}
	updatedBy := "api"
	setting := model.AppSetting{Key: "watchdog.worker", Value: value, UpdatedBy: &updatedBy}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"value":      value,
			"updated_by": updatedBy,
			"updated_at": gorm.Expr("now()"),
		}),
	}).Create(&setting).Error
}
