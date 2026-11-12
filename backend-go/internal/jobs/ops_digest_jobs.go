package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/operationsdigest"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type OpsDigestScanArgs struct{}

func (OpsDigestScanArgs) Kind() string { return "myhot_ops_digest_scan" }

type OpsDigestArgs struct {
	Date string `json:"date"`
}

func (OpsDigestArgs) Kind() string { return "myhot_ops_digest" }

type OpsDigestService interface {
	Due(context.Context, time.Time) (string, bool, error)
	Run(context.Context, time.Time) (operationsdigest.Result, error)
}

type OpsDigestScanWorker struct {
	river.WorkerDefaults[OpsDigestScanArgs]
	Service OpsDigestService
	Client  *river.Client[pgx.Tx]
}

type OpsDigestWorker struct {
	river.WorkerDefaults[OpsDigestArgs]
	Service OpsDigestService
	DB      *gorm.DB
}

func opsDigestInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func (w *OpsDigestScanWorker) Work(ctx context.Context, _ *river.Job[OpsDigestScanArgs]) error {
	date, due, err := w.Service.Due(ctx, time.Now())
	if err != nil || !due {
		return err
	}
	_, err = w.Client.Insert(ctx, OpsDigestArgs{Date: date}, opsDigestInsertOpts())
	return err
}

func (w *OpsDigestWorker) Work(ctx context.Context, _ *river.Job[OpsDigestArgs]) error {
	return runTrackedTask(ctx, w.DB, "ops.digest", func() (any, error) {
		return w.Service.Run(ctx, time.Now())
	})
}

func (w *OpsDigestWorker) Timeout(*river.Job[OpsDigestArgs]) time.Duration {
	return 2 * time.Minute
}

var _ OpsDigestService = (*operationsdigest.Service)(nil)
