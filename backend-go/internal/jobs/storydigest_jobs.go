package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/jobargs"
	"aihot/backend-go/internal/storydigest"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type StoryDigestScanArgs struct{}

func (StoryDigestScanArgs) Kind() string { return "myhot_story_digest_scan" }

type StoryDigestArgs = jobargs.StoryDigestArgs

type StoryDigestScanWorker struct {
	river.WorkerDefaults[StoryDigestScanArgs]
	Service *storydigest.Service
	Client  *river.Client[pgx.Tx]
}

type StoryDigestWorker struct {
	river.WorkerDefaults[StoryDigestArgs]
	Service *storydigest.Service
}

func (w *StoryDigestScanWorker) Work(ctx context.Context, _ *river.Job[StoryDigestScanArgs]) error {
	return w.Service.Scan(ctx, 100, func(ctx context.Context, id int64) error {
		_, err := w.Client.Insert(ctx, StoryDigestArgs{StoryID: id}, &river.InsertOpts{
			Queue: "story_digests", Priority: 2, MaxAttempts: 4,
			UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			}},
		})
		return err
	})
}

func (w *StoryDigestWorker) Work(ctx context.Context, job *river.Job[StoryDigestArgs]) error {
	_, err := w.Service.Compose(ctx, job.Args.StoryID, false)
	return err
}

func (w *StoryDigestWorker) Timeout(*river.Job[StoryDigestArgs]) time.Duration {
	return 4 * time.Minute
}

func storyDigestPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		return StoryDigestScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
	}, &river.PeriodicJobOpts{ID: "myhot-story-digest-scan", RunOnStart: true})
}
