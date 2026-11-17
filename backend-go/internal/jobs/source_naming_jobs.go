package jobs

import (
	"context"

	"aihot/backend-go/internal/sourcenaming"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type SourceNamingArgs struct {
	SourceID string `json:"sourceId" river:"unique"`
}

/** Kind identifies a shared-name task. */
func (SourceNamingArgs) Kind() string { return "myhot_source_naming" }

type SourceNamingWorker struct {
	river.WorkerDefaults[SourceNamingArgs]
	Service *sourcenaming.Service
}

/** Work attempts to name a source independently of collection. */
func (w *SourceNamingWorker) Work(ctx context.Context, job *river.Job[SourceNamingArgs]) error {
	return w.Service.Name(ctx, job.Args.SourceID)
}

/** enqueueSourceNames dispatches a naming task for each ready source. */
func enqueueSourceNames(ctx context.Context, service *sourcenaming.Service, client *river.Client[pgx.Tx], sourceID string) error {
	if service == nil || client == nil {
		return nil
	}
	ids, err := service.ReadyIDs(ctx, sourceID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		_, err := client.Insert(ctx, SourceNamingArgs{SourceID: id}, &river.InsertOpts{
			Queue:       "source_naming",
			MaxAttempts: 5,
			UniqueOpts:  sourceInsertOpts().UniqueOpts,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
