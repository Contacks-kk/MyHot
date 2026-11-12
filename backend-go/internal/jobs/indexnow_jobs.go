package jobs

import (
	"context"
	"time"
	_ "time/tzdata"

	"aihot/backend-go/internal/indexnow"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

type IndexNowArgs struct{}

func (IndexNowArgs) Kind() string { return "myhot_seo_indexnow" }

type IndexNowWorker struct {
	river.WorkerDefaults[IndexNowArgs]
	Service *indexnow.Service
	DB      *gorm.DB
}

func indexNowInsertOpts() *river.InsertOpts {
	// A short uniqueness period coalesces startup catch-up with a due wall-clock run
	// without suppressing tomorrow's 05:50 slot.
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
}

func (w *IndexNowWorker) Work(ctx context.Context, _ *river.Job[IndexNowArgs]) error {
	return runTrackedTask(ctx, w.DB, "seo.indexnow", func() (any, error) {
		return w.Service.Run(ctx, time.Now())
	})
}

func (w *IndexNowWorker) Timeout(*river.Job[IndexNowArgs]) time.Duration {
	return 40 * time.Second
}

type indexNowSchedule struct {
	location *time.Location
	hour     int
	minute   int
}

func (s indexNowSchedule) Next(current time.Time) time.Time {
	now := current.In(s.location)
	next := time.Date(now.Year(), now.Month(), now.Day(), s.hour, s.minute, 0, 0, s.location)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func newIndexNowSchedule() (indexNowSchedule, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return indexNowSchedule{}, err
	}
	return indexNowSchedule{location: location, hour: 5, minute: 50}, nil
}

func indexNowPeriodicJob(schedule indexNowSchedule) *river.PeriodicJob {
	return river.NewPeriodicJob(schedule, func() (river.JobArgs, *river.InsertOpts) {
		return IndexNowArgs{}, indexNowInsertOpts()
	}, indexNowPeriodicOptions())
}

func indexNowPeriodicOptions() *river.PeriodicJobOpts {
	return &river.PeriodicJobOpts{ID: "myhot-seo-indexnow", RunOnStart: true}
}
