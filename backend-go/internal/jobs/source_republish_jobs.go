package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/hotranking"
	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type SourceRepublishArgs struct {
	SourceID string `json:"sourceId" river:"unique"`
}

func (SourceRepublishArgs) Kind() string { return "myhot_republish_source" }

func SourceRepublishInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: "source_republish", Priority: 2, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{
		ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
			rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		},
	}}
}

type SourceRepublishWorker struct {
	river.WorkerDefaults[SourceRepublishArgs]
	Service    *publication.Service
	HotRanking *hotranking.Service
	DB         *gorm.DB
}

func (w *SourceRepublishWorker) Work(ctx context.Context, job *river.Job[SourceRepublishArgs]) error {
	return runTrackedTask(ctx, w.DB, "publication.republish-source", func() (any, error) {
		return w.Service.RepublishSource(ctx, job.Args.SourceID, nil, func(result publication.RepublishResult) error {
			if result.Reduced == 0 {
				return nil
			}
			_, err := w.HotRanking.Compute(ctx, time.Now())
			return err
		})
	})
}

func (*SourceRepublishWorker) Timeout(*river.Job[SourceRepublishArgs]) time.Duration {
	return 30 * time.Minute
}

type SourceRepublishScanArgs struct{}

func (SourceRepublishScanArgs) Kind() string { return "myhot_republish_source_scan" }

type SourceRepublishScanWorker struct {
	river.WorkerDefaults[SourceRepublishScanArgs]
	Service *publication.Service
	Client  *river.Client[pgx.Tx]
	DB      *gorm.DB
}

func (w *SourceRepublishScanWorker) Work(ctx context.Context, _ *river.Job[SourceRepublishScanArgs]) error {
	return runTrackedTask(ctx, w.DB, "publication.republish-source.scan", func() (any, error) {
		sourceIDs, err := w.Service.QueuedSourceRepublishes(ctx)
		if err != nil {
			return nil, err
		}
		for _, sourceID := range sourceIDs {
			if _, err := w.Client.Insert(ctx, SourceRepublishArgs{SourceID: sourceID}, SourceRepublishInsertOpts()); err != nil {
				return nil, err
			}
		}
		return map[string]int{"enqueued": len(sourceIDs)}, nil
	})
}

func sourceRepublishPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		return SourceRepublishScanArgs{}, &river.InsertOpts{Priority: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
	}, &river.PeriodicJobOpts{ID: "myhot-source-republish-scan", RunOnStart: true})
}
