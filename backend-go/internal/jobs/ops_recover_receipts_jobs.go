package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/receiptrecovery"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type ReceiptRecoveryArgs struct{}

func (ReceiptRecoveryArgs) Kind() string { return "myhot_ops_recover_receipts" }

type ReceiptRecoveryWorker struct {
	river.WorkerDefaults[ReceiptRecoveryArgs]
	Service *receiptrecovery.Service
	DB      *gorm.DB
}

func (w *ReceiptRecoveryWorker) Work(ctx context.Context, _ *river.Job[ReceiptRecoveryArgs]) error {
	return runTrackedTask(ctx, w.DB, "ops.recover.receipts", func() (any, error) {
		return w.Service.Recover(ctx, time.Now())
	})
}

func (w *ReceiptRecoveryWorker) Timeout(*river.Job[ReceiptRecoveryArgs]) time.Duration {
	return 2 * time.Minute
}

func receiptRecoveryInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByPeriod: 10 * time.Minute, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func receiptRecoveryPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(tenMinuteSchedule{}, func() (river.JobArgs, *river.InsertOpts) {
		return ReceiptRecoveryArgs{}, receiptRecoveryInsertOpts()
	}, &river.PeriodicJobOpts{ID: "myhot-receipt-recovery"})
}
