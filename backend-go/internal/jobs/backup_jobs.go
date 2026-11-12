package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/backup"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type BackupScanArgs struct{}

func (BackupScanArgs) Kind() string { return "myhot_backup_scan" }

type BackupArgs struct {
	Date string `json:"date"`
}

func (BackupArgs) Kind() string { return "myhot_backup" }

type BackupService interface {
	Configured() bool
	Due(context.Context, time.Time) (string, bool, error)
	Run(context.Context, string, time.Time) error
}

type BackupScanWorker struct {
	river.WorkerDefaults[BackupScanArgs]
	Service BackupService
	Client  *river.Client[pgx.Tx]
}

type BackupWorker struct {
	river.WorkerDefaults[BackupArgs]
	Service BackupService
}

func backupInsertOpts() *river.InsertOpts {
	// The former pg-boss schedule used retryLimit=1: initial attempt plus one retry.
	return &river.InsertOpts{
		Priority:    1,
		MaxAttempts: 2,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			}}}
}

func (w *BackupScanWorker) Work(ctx context.Context, _ *river.Job[BackupScanArgs]) error {
	if w.Service == nil || !w.Service.Configured() {
		return nil
	}
	date, due, err := w.Service.Due(ctx, time.Now())
	if err != nil || !due {
		return err
	}
	_, err = w.Client.Insert(ctx, BackupArgs{Date: date}, backupInsertOpts())
	return err
}

func (w *BackupWorker) Work(ctx context.Context, job *river.Job[BackupArgs]) error {
	return w.Service.Run(ctx, job.Args.Date, time.Now())
}

func (w *BackupWorker) Timeout(*river.Job[BackupArgs]) time.Duration { return time.Hour }

var _ BackupService = (*backup.Service)(nil)
