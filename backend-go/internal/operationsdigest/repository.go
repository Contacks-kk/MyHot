package operationsdigest

import (
	"context"
	"encoding/json"
	"time"

	"aihot/backend-go/internal/sourcehealth"
	"gorm.io/gorm"
)

type GormRepository struct {
	db           *gorm.DB
	sourceHealth sourcehealth.Repository
}

func NewRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db, sourceHealth: sourcehealth.NewRepository(db)}
}

func (r *GormRepository) HasRun(ctx context.Context, since, before time.Time) (bool, error) {
	var found bool
	err := r.db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1 FROM job_runs
			WHERE job = 'ops.digest' AND started_at >= ? AND started_at < ?
		)`, since, before).Scan(&found).Error
	return found, err
}

func (r *GormRepository) Digest(ctx context.Context, now time.Time) (Snapshot, error) {
	var snapshot Snapshot
	snapshot.SourceHealthSince = now.Add(-7 * 24 * time.Hour)
	weekly, err := r.sourceHealth.Weekly(ctx, snapshot.SourceHealthSince, now.Add(-14*24*time.Hour))
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.SourceHealth = weekly

	var receipts struct {
		Count    int     `gorm:"column:count"`
		Services *string `gorm:"column:services"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT count(*)::int AS count,
		       string_agg(DISTINCT service || '/' || purpose, '、') AS services
		FROM receipts WHERE status = 'unknown'`).Scan(&receipts).Error; err != nil {
		return Snapshot{}, err
	}
	snapshot.UnknownReceipts, snapshot.UnknownServices = receipts.Count, receipts.Services
	if err := r.db.WithContext(ctx).Raw(`SELECT count(*)::int FROM deliveries WHERE status = 'unknown'`).Scan(&snapshot.UnknownDeliveries).Error; err != nil {
		return Snapshot{}, err
	}

	if err := r.readBackup(ctx, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := r.readImageBudget(ctx, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := r.readQueues(ctx, now, &snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (r *GormRepository) readQueues(ctx context.Context, now time.Time, snapshot *Snapshot) error {
	var queues []Queue
	if err := r.db.WithContext(ctx).Raw(`
		SELECT 'River/' || kind AS name, count(*)::int AS count, min(scheduled_at) AS oldest
		FROM river_job
		WHERE state IN ('available', 'retryable') AND scheduled_at <= ?
		GROUP BY kind ORDER BY kind`, now).Scan(&queues).Error; err != nil {
		return err
	}
	var pgBossExists bool
	if err := r.db.WithContext(ctx).Raw(`SELECT to_regclass('pgboss.job') IS NOT NULL`).Scan(&pgBossExists).Error; err != nil {
		return err
	}
	if pgBossExists {
		var legacy []Queue
		if err := r.db.WithContext(ctx).Raw(`
			SELECT name, count(*)::int AS count, min(start_after) AS oldest
			FROM pgboss.job
			WHERE state IN ('created', 'retry') AND start_after <= ? AND name NOT LIKE 'cron.%'
			GROUP BY name ORDER BY name`, now).Scan(&legacy).Error; err != nil {
			return err
		}
		queues = append(queues, legacy...)
	}
	snapshot.Queues = queues
	return nil
}

func (r *GormRepository) readBackup(ctx context.Context, snapshot *Snapshot) error {
	var setting struct {
		Value json.RawMessage `gorm:"column:value"`
	}
	result := r.db.WithContext(ctx).Raw(`SELECT value FROM settings WHERE key = 'backup.last'`).Scan(&setting)
	if result.Error != nil {
		return result.Error
	}
	if len(setting.Value) > 0 {
		var state BackupState
		if err := json.Unmarshal(setting.Value, &state); err != nil {
			return err
		}
		snapshot.Backup = &state
	}
	var runs struct {
		LastSuccess  *time.Time `gorm:"column:last_success"`
		FirstAttempt *time.Time `gorm:"column:first_attempt"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT max(finished_at) FILTER (WHERE status = 'ok') AS last_success,
		       min(started_at) AS first_attempt
		FROM job_runs WHERE job = 'ops.backup'`).Scan(&runs).Error; err != nil {
		return err
	}
	snapshot.LastBackupSuccess, snapshot.FirstBackupAttempt = runs.LastSuccess, runs.FirstAttempt
	return nil
}

func (r *GormRepository) readImageBudget(ctx context.Context, snapshot *Snapshot) error {
	var setting struct {
		Value     json.RawMessage `gorm:"column:value"`
		UpdatedAt time.Time       `gorm:"column:updated_at"`
	}
	result := r.db.WithContext(ctx).Raw(`SELECT value, updated_at FROM settings WHERE key = 'egress.imgproxy'`).Scan(&setting)
	if result.Error != nil {
		return result.Error
	}
	if len(setting.Value) == 0 {
		return nil
	}
	var budget ImageBudget
	if err := json.Unmarshal(setting.Value, &budget); err != nil {
		return err
	}
	budget.UpdatedAt = setting.UpdatedAt
	snapshot.ImageBudget = &budget
	return nil
}

var _ Repository = (*GormRepository)(nil)
