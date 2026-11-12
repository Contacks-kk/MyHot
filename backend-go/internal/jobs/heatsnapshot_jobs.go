package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/heatsnapshot"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type HeatSnapshotArgs struct{}

func (HeatSnapshotArgs) Kind() string { return "myhot_heat_snapshot" }

type HeatSnapshotWorker struct {
	river.WorkerDefaults[HeatSnapshotArgs]
	Service *heatsnapshot.Service
	DB      *gorm.DB
}

func heatSnapshotInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func (w *HeatSnapshotWorker) Work(ctx context.Context, _ *river.Job[HeatSnapshotArgs]) error {
	return runTrackedTask(ctx, w.DB, "hot.snapshot", func() (any, error) {
		return w.Service.Snapshot(ctx, time.Now())
	})
}

func (w *HeatSnapshotWorker) Timeout(*river.Job[HeatSnapshotArgs]) time.Duration {
	return 2 * time.Minute
}

type heatSnapshotSchedule struct {
	location *time.Location
}

func (s heatSnapshotSchedule) Next(current time.Time) time.Time {
	now := current.In(s.location)
	next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 2, 0, 0, s.location)
	if !next.After(now) {
		next = next.Add(time.Hour)
	}
	return next
}

func newHeatSnapshotSchedule() (heatSnapshotSchedule, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return heatSnapshotSchedule{}, err
	}
	return heatSnapshotSchedule{location: location}, nil
}

func heatSnapshotPeriodicJob(schedule heatSnapshotSchedule) *river.PeriodicJob {
	return river.NewPeriodicJob(schedule, func() (river.JobArgs, *river.InsertOpts) {
		return HeatSnapshotArgs{}, heatSnapshotInsertOpts()
	}, heatSnapshotPeriodicOptions())
}

func heatSnapshotPeriodicOptions() *river.PeriodicJobOpts {
	return &river.PeriodicJobOpts{ID: "myhot-hot-snapshot"}
}
