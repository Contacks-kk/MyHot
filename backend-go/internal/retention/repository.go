package retention

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

func (r *GormRepository) HasRun(ctx context.Context, job string, start, end time.Time) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.JobRun{}).Where("job = ? AND started_at >= ? AND started_at < ?", job, start, end).Count(&count).Error
	return count > 0, err
}

func (r *GormRepository) BeginRun(ctx context.Context, job string, start, end time.Time) (int64, bool, error) {
	var runID int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", job+":"+start.Format("2006-01-02")).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.JobRun{}).Where("job = ? AND started_at >= ? AND started_at < ? AND (status = 'ok' OR (status = 'running' AND started_at >= ?))", job, start, end, time.Now().Add(-StuckRunGrace)).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		run := model.JobRun{Job: job, StartedAt: time.Now(), Status: "running"}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		runID = run.ID
		return nil
	})
	return runID, runID != 0, err
}

func (r *GormRepository) CleanupDatabase(ctx context.Context, now time.Time) (leases, runs int64, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		leaseResult := tx.Exec("DELETE FROM delivery_leases WHERE expires_at < ?", now)
		if leaseResult.Error != nil {
			return leaseResult.Error
		}
		runResult := tx.Exec(`DELETE FROM job_runs WHERE started_at < ? AND (status IS DISTINCT FROM 'failed' OR started_at < ?)`, now.Add(-30*24*time.Hour), now.Add(-90*24*time.Hour))
		if runResult.Error != nil {
			return runResult.Error
		}
		leases, runs = leaseResult.RowsAffected, runResult.RowsAffected
		return nil
	})
	return leases, runs, err
}

func (r *GormRepository) CompleteRun(ctx context.Context, id int64, detail json.RawMessage) error {
	return r.db.WithContext(ctx).Model(&model.JobRun{}).Where("id = ?", id).Updates(map[string]any{
		"status": "ok", "finished_at": gorm.Expr("now()"), "detail": gorm.Expr("?::jsonb", string(detail)),
	}).Error
}

func (r *GormRepository) FailRun(ctx context.Context, id int64, message string) error {
	return r.db.WithContext(ctx).Model(&model.JobRun{}).Where("id = ?", id).Updates(map[string]any{
		"status": "failed", "finished_at": gorm.Expr("now()"), "error": truncateError(errors.New(message)),
	}).Error
}
