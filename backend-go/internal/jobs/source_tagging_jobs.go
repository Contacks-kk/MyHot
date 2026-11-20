package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/tagging"

	"github.com/riverqueue/river"
)

/** SourceTaggingArgs retains the queue identity for the label maintenance job. */
type SourceTaggingArgs struct {
	SourceID string `json:"sourceId,omitempty" river:"unique"`
}

func (SourceTaggingArgs) Kind() string { return "myhot_source_tagging" }

/** SourceTaggingWorker indexes articles and maintains the shared label lifecycle. */
type SourceTaggingWorker struct {
	river.WorkerDefaults[SourceTaggingArgs]
	Service *tagging.Service
}

/** Work runs one bounded maintenance round. */
func (w *SourceTaggingWorker) Work(ctx context.Context, _ *river.Job[SourceTaggingArgs]) error {
	if w.Service == nil {
		return nil
	}
	return w.Service.Run(ctx, time.Now())
}

func (w *SourceTaggingWorker) Timeout(*river.Job[SourceTaggingArgs]) time.Duration {
	return 2 * time.Minute
}
