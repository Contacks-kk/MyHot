package jobs

import (
	"context"
	"errors"
	"time"

	"aihot/backend-go/internal/media"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type MediaPrepareArgs struct {
	URL       string `json:"url,omitempty"`
	Mode      string `json:"mode,omitempty"`
	ArticleID string `json:"articleId,omitempty"`
}

func (MediaPrepareArgs) Kind() string { return "media.prepare" }

type MediaPrepareWorker struct {
	river.WorkerDefaults[MediaPrepareArgs]
	Service  *media.Service
	Articles *ArticleMediaPrewarmer
}

func (w *MediaPrepareWorker) Work(ctx context.Context, job *river.Job[MediaPrepareArgs]) error {
	if time.Since(job.CreatedAt) >= 10*time.Minute {
		return nil
	}
	if job.Args.ArticleID != "" {
		if w.Articles == nil {
			return errors.New("article media prewarmer is unavailable")
		}
		_, err := w.Articles.Prepare(ctx, job.Args.ArticleID)
		return err
	}
	if job.Args.URL == "" || job.Args.Mode == "" {
		return nil
	}
	_, err := w.Service.ConvertAnimated(ctx, job.Args.URL, job.Args.Mode)
	return err
}

func (w *MediaPrepareWorker) NextRetry(*river.Job[MediaPrepareArgs]) time.Time {
	return time.Now().Add(120 * time.Second)
}

func (w *MediaPrepareWorker) Timeout(*river.Job[MediaPrepareArgs]) time.Duration {
	return 10 * time.Minute
}

func InsertMediaPrepare(ctx context.Context, pool *pgxpool.Pool, rawURL, mode string) error {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return err
	}
	_, err = client.Insert(ctx, MediaPrepareArgs{URL: rawURL, Mode: mode}, &river.InsertOpts{
		Queue: "media", MaxAttempts: 2,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
			rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		}},
	})
	return err
}
