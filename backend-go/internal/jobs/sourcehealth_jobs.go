package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/sourcehealth"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type SourceHealthScanArgs struct{}

func (SourceHealthScanArgs) Kind() string { return "myhot_source_health_scan" }

type SourceHealthArgs struct {
	Week string `json:"week" river:"unique"`
}

func (SourceHealthArgs) Kind() string { return "myhot_source_health_report" }

type SourceHealthService interface {
	Due(context.Context, time.Time) (string, bool, error)
	Run(context.Context, time.Time) (sourcehealth.Result, error)
}

type SourceHealthScanWorker struct {
	river.WorkerDefaults[SourceHealthScanArgs]
	Service SourceHealthService
	Client  *river.Client[pgx.Tx]
}

type SourceHealthWorker struct {
	river.WorkerDefaults[SourceHealthArgs]
	Service SourceHealthService
	DB      *gorm.DB
}

func sourceHealthInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func (w *SourceHealthScanWorker) Work(ctx context.Context, _ *river.Job[SourceHealthScanArgs]) error {
	week, due, err := w.Service.Due(ctx, time.Now())
	if err != nil || !due {
		return err
	}
	_, err = w.Client.Insert(ctx, SourceHealthArgs{Week: week}, sourceHealthInsertOpts())
	return err
}

func (w *SourceHealthWorker) Work(ctx context.Context, _ *river.Job[SourceHealthArgs]) error {
	return runTrackedTask(ctx, w.DB, "reports.source-health", func() (any, error) {
		return w.Service.Run(ctx, time.Now())
	})
}

func (w *SourceHealthWorker) Timeout(*river.Job[SourceHealthArgs]) time.Duration {
	return 2 * time.Minute
}

var _ SourceHealthService = (*sourcehealth.Service)(nil)
