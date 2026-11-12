package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/retention"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type RetentionScanArgs struct{}

func (RetentionScanArgs) Kind() string { return "myhot_retention_scan" }

type RetentionArgs struct {
	Date string `json:"date"`
}

func (RetentionArgs) Kind() string { return "myhot_retention" }

type RetentionService interface {
	Due(context.Context, time.Time) (string, bool, error)
	Run(context.Context, string, time.Time) error
}

type RetentionScanWorker struct {
	river.WorkerDefaults[RetentionScanArgs]
	Service RetentionService
	Client  *river.Client[pgx.Tx]
}

type RetentionWorker struct {
	river.WorkerDefaults[RetentionArgs]
	Service RetentionService
}

func retentionInsertOpts() *river.InsertOpts {
	// pg-boss cron queues use retryLimit: 1, which permits the initial attempt plus one retry.
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func (w *RetentionScanWorker) Work(ctx context.Context, _ *river.Job[RetentionScanArgs]) error {
	date, due, err := w.Service.Due(ctx, time.Now())
	if err != nil || !due {
		return err
	}
	_, err = w.Client.Insert(ctx, RetentionArgs{Date: date}, retentionInsertOpts())
	return err
}

func (w *RetentionWorker) Work(ctx context.Context, job *river.Job[RetentionArgs]) error {
	return w.Service.Run(ctx, job.Args.Date, time.Now())
}

func (w *RetentionWorker) Timeout(*river.Job[RetentionArgs]) time.Duration {
	return 2 * time.Minute
}

var _ RetentionService = (*retention.Service)(nil)
