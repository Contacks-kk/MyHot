package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/storylinks"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type StoryLinksArgs struct{}

func (StoryLinksArgs) Kind() string { return "myhot_story_links" }

type StoryLinksWorker struct {
	river.WorkerDefaults[StoryLinksArgs]
	Service *storylinks.Service
	DB      *gorm.DB
}

func storyLinksInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func (w *StoryLinksWorker) Work(ctx context.Context, _ *river.Job[StoryLinksArgs]) error {
	return runTrackedTask(ctx, w.DB, "stories.links", func() (any, error) {
		return w.Service.Run(ctx)
	})
}

func (w *StoryLinksWorker) Timeout(*river.Job[StoryLinksArgs]) time.Duration { return 2 * time.Minute }

type storyLinksSchedule struct {
	location *time.Location
}

func (s storyLinksSchedule) Next(current time.Time) time.Time {
	now := current.In(s.location)
	next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 12, 0, 0, s.location)
	if !next.After(now) {
		next = next.Add(time.Hour)
	}
	return next
}

func newStoryLinksSchedule() (storyLinksSchedule, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return storyLinksSchedule{}, err
	}
	return storyLinksSchedule{location: location}, nil
}

func storyLinksPeriodicJob(schedule storyLinksSchedule) *river.PeriodicJob {
	return river.NewPeriodicJob(schedule, func() (river.JobArgs, *river.InsertOpts) {
		return StoryLinksArgs{}, storyLinksInsertOpts()
	}, storyLinksPeriodicOptions())
}

func storyLinksPeriodicOptions() *river.PeriodicJobOpts {
	return &river.PeriodicJobOpts{ID: "myhot-story-links"}
}
