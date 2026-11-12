package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/operationsalerts"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type OpsAlertsArgs struct{}

func (OpsAlertsArgs) Kind() string { return "myhot_ops_alerts" }

type OpsAlertsService interface {
	Check(context.Context, time.Time) (operationsalerts.Result, error)
}

type OpsAlertsWorker struct {
	river.WorkerDefaults[OpsAlertsArgs]
	Service OpsAlertsService
	DB      *gorm.DB
}

func (w *OpsAlertsWorker) Work(ctx context.Context, _ *river.Job[OpsAlertsArgs]) error {
	return runTrackedTask(ctx, w.DB, "ops.alerts", func() (any, error) {
		return w.Service.Check(ctx, time.Now())
	})
}

func (w *OpsAlertsWorker) Timeout(*river.Job[OpsAlertsArgs]) time.Duration {
	return 2 * time.Minute
}

func opsAlertsInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func opsAlertsPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(tenMinuteSchedule{}, func() (river.JobArgs, *river.InsertOpts) {
		return OpsAlertsArgs{}, opsAlertsInsertOpts()
	}, &river.PeriodicJobOpts{ID: "myhot-ops-alerts"})
}

var _ OpsAlertsService = (*operationsalerts.Service)(nil)
