package jobs

import (
	"context"
	"time"
	_ "time/tzdata"

	"aihot/backend-go/internal/reports"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

type ReportComposeArgs struct{}

func (ReportComposeArgs) Kind() string { return "myhot_report_compose" }

type ReportComposeWorker struct {
	river.WorkerDefaults[ReportComposeArgs]
	Service *reports.Service
	DB      *gorm.DB
}

func (w *ReportComposeWorker) Work(ctx context.Context, _ *river.Job[ReportComposeArgs]) error {
	return runTrackedTask(ctx, w.DB, "reports.compose", func() (any, error) {
		return w.Service.ComposeDue(ctx, time.Now(), 8)
	})
}

func (w *ReportComposeWorker) Timeout(*river.Job[ReportComposeArgs]) time.Duration {
	return 30 * time.Minute
}

type halfHourSchedule struct{ location *time.Location }

func (s halfHourSchedule) Next(current time.Time) time.Time {
	now := current.In(s.location)
	if now.Minute() < 30 {
		return time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 30, 0, 0, s.location)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, s.location)
}

func newHalfHourSchedule() (river.PeriodicSchedule, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	return halfHourSchedule{location: location}, nil
}

func reportComposeInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: "reports", Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByPeriod: 30 * time.Minute}}
}

func reportComposePeriodicJob(schedule river.PeriodicSchedule) *river.PeriodicJob {
	return river.NewPeriodicJob(schedule, func() (river.JobArgs, *river.InsertOpts) {
		return ReportComposeArgs{}, reportComposeInsertOpts()
	}, &river.PeriodicJobOpts{ID: "myhot-reports-compose", RunOnStart: true})
}

var _ river.Worker[ReportComposeArgs] = (*ReportComposeWorker)(nil)
