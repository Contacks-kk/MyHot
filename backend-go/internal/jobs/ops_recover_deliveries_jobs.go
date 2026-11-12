package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/admindeliveries"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type DeliveryRecoveryArgs struct{}

func (DeliveryRecoveryArgs) Kind() string { return "myhot_ops_recover_deliveries" }

type DeliveryRecoveryService interface {
	RecoverStale(context.Context, time.Time) (admindeliveries.StaleCounts, error)
}

type DeliveryRecoveryWorker struct {
	river.WorkerDefaults[DeliveryRecoveryArgs]
	Service DeliveryRecoveryService
	DB      *gorm.DB
}

func (w *DeliveryRecoveryWorker) Work(ctx context.Context, _ *river.Job[DeliveryRecoveryArgs]) error {
	return runTrackedTask(ctx, w.DB, "ops.recover.deliveries", func() (any, error) {
		return w.Service.RecoverStale(ctx, time.Now())
	})
}

func (w *DeliveryRecoveryWorker) Timeout(*river.Job[DeliveryRecoveryArgs]) time.Duration {
	return time.Minute
}

func deliveryRecoveryInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByPeriod: 10 * time.Minute, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func deliveryRecoveryPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(tenMinuteSchedule{}, func() (river.JobArgs, *river.InsertOpts) {
		return DeliveryRecoveryArgs{}, deliveryRecoveryInsertOpts()
	}, &river.PeriodicJobOpts{ID: "myhot-delivery-recovery"})
}

var _ DeliveryRecoveryService = (*admindeliveries.Service)(nil)
