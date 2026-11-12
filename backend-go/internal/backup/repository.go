package backup

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) HasRun(ctx context.Context, start, end time.Time) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.JobRun{}).
		Where("job = ? AND started_at >= ? AND started_at < ? AND (status <> 'running' OR started_at >= ?)", JobName, start, end, time.Now().Add(-stuckRunGrace)).
		Count(&count).Error
	return count > 0, err
}

func (r *GormRepository) BeginRun(ctx context.Context, start, end time.Time) (int64, bool, error) {
	var id int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		day := start.Format("2006-01-02")
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", JobName+":"+day).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.JobRun{}).
			Where("job = ? AND started_at >= ? AND started_at < ? AND (status = 'ok' OR (status = 'running' AND started_at >= ?))", JobName, start, end, time.Now().Add(-stuckRunGrace)).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		run := model.JobRun{Job: JobName, StartedAt: time.Now(), Status: "running"}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		id = run.ID
		return nil
	})
	return id, id != 0, err
}

func (r *GormRepository) CompleteRun(ctx context.Context, id int64, detail json.RawMessage) error {
	return r.db.WithContext(ctx).Model(&model.JobRun{}).Where("id = ?", id).Updates(map[string]any{
		"status": "ok", "finished_at": gorm.Expr("now()"), "detail": gorm.Expr("?::jsonb", string(detail)),
	}).Error
}

func (r *GormRepository) FailRun(ctx context.Context, id int64, message string) error {
	if len([]rune(message)) > 4000 {
		message = string([]rune(message)[:4000])
	}
	return r.db.WithContext(ctx).Model(&model.JobRun{}).Where("id = ?", id).Updates(map[string]any{
		"status": "failed", "finished_at": gorm.Expr("now()"), "error": message,
	}).Error
}

func (r *GormRepository) SaveLast(ctx context.Context, summary json.RawMessage) error {
	if !json.Valid(summary) {
		return errors.New("invalid backup summary")
	}
	return r.db.WithContext(ctx).Exec(`INSERT INTO settings(key,value,updated_by) VALUES ('backup.last',?::jsonb,'backup') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now()`, string(summary)).Error
}

var _ Repository = (*GormRepository)(nil)
