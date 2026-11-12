package jobs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type FeedbackScanArgs struct{}

func (FeedbackScanArgs) Kind() string { return "myhot_feedback_scan" }

type FeedbackForwardArgs struct {
	FeedbackID int64 `json:"feedbackId" river:"unique"`
}

func (FeedbackForwardArgs) Kind() string { return "myhot_feedback_forward" }

type FeedbackForwardService interface {
	PendingForwardIDs(context.Context, time.Time) ([]int64, error)
	Forward(context.Context, int64) error
}

type FeedbackScanWorker struct {
	river.WorkerDefaults[FeedbackScanArgs]
	Service FeedbackForwardService
	Client  *river.Client[pgx.Tx]
	Enabled bool
}

type FeedbackForwardWorker struct {
	river.WorkerDefaults[FeedbackForwardArgs]
	Service FeedbackForwardService
}

func FeedbackForwardInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func InsertFeedbackForward(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return err
	}
	_, err = client.Insert(ctx, FeedbackForwardArgs{FeedbackID: id}, FeedbackForwardInsertOpts())
	return err
}

func (w *FeedbackScanWorker) Work(ctx context.Context, _ *river.Job[FeedbackScanArgs]) error {
	if !w.Enabled {
		return nil
	}
	ids, err := w.Service.PendingForwardIDs(ctx, time.Now())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := w.Client.Insert(ctx, FeedbackForwardArgs{FeedbackID: id}, FeedbackForwardInsertOpts()); err != nil {
			return err
		}
	}
	return nil
}

func (w *FeedbackForwardWorker) Work(ctx context.Context, job *river.Job[FeedbackForwardArgs]) error {
	return w.Service.Forward(ctx, job.Args.FeedbackID)
}

func (w *FeedbackForwardWorker) Timeout(*river.Job[FeedbackForwardArgs]) time.Duration {
	return time.Minute
}

type tenMinuteSchedule struct{}

func (tenMinuteSchedule) Next(current time.Time) time.Time {
	return current.Truncate(10 * time.Minute).Add(10 * time.Minute)
}
