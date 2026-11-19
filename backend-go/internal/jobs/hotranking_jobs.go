package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/hotranking"
	"aihot/backend-go/internal/jobargs"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type HotRankingArgs = jobargs.HotRankingArgs

type HotRankingWorker struct {
	river.WorkerDefaults[HotRankingArgs]
	Service *hotranking.Service
	DB      *gorm.DB
}

func hotRankingInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func (w *HotRankingWorker) Work(ctx context.Context, _ *river.Job[HotRankingArgs]) error {
	return runTrackedTask(ctx, w.DB, "hot.rank", func() (any, error) {
		return w.Service.Compute(ctx, time.Now())
	})
}

func (w *HotRankingWorker) Timeout(*river.Job[HotRankingArgs]) time.Duration {
	return 2 * time.Minute
}

type hotRankingSchedule struct {
	location *time.Location
}

func (s hotRankingSchedule) Next(current time.Time) time.Time {
	now := current.In(s.location)
	next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), 0, 0, s.location)
	minutes := 5 - next.Minute()%5
	next = next.Add(time.Duration(minutes) * time.Minute)
	if !next.After(now) {
		next = next.Add(5 * time.Minute)
	}
	return next
}

func newHotRankingSchedule() (hotRankingSchedule, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return hotRankingSchedule{}, err
	}
	return hotRankingSchedule{location: location}, nil
}

func hotRankingPeriodicJob(schedule hotRankingSchedule) *river.PeriodicJob {
	return river.NewPeriodicJob(schedule, func() (river.JobArgs, *river.InsertOpts) {
		return HotRankingArgs{}, hotRankingInsertOpts()
	}, hotRankingPeriodicOptions())
}

func hotRankingPeriodicOptions() *river.PeriodicJobOpts {
	return &river.PeriodicJobOpts{ID: "myhot-hot-ranking"}
}
