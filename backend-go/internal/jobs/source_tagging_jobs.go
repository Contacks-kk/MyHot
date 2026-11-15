package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/tagging"

	"github.com/riverqueue/river"
)

/** SourceTaggingArgs represents an asynchronous source auto-labeling job. */
type SourceTaggingArgs struct {
	SourceID string `json:"sourceId" river:"unique"`
}

func (SourceTaggingArgs) Kind() string { return "myhot_source_tagging" }

/** SourceTaggingWorker processes auto-labeling for sources in the background. */
type SourceTaggingWorker struct {
	river.WorkerDefaults[SourceTaggingArgs]
	Service *tagging.Service
}

func (w *SourceTaggingWorker) Work(ctx context.Context, job *river.Job[SourceTaggingArgs]) error {
	if w.Service == nil {
		return nil
	}
	return w.Service.TagSource(ctx, job.Args.SourceID)
}

func (w *SourceTaggingWorker) Timeout(*river.Job[SourceTaggingArgs]) time.Duration {
	return 2 * time.Minute
}
