package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"
	_ "time/tzdata"

	"aihot/backend-go/internal/collector"
	"aihot/backend-go/internal/mpcollector"
	"aihot/backend-go/internal/sourceicons"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

type SourceScanArgs struct{}

func (SourceScanArgs) Kind() string { return "myhot_source_scan" }

type SourceIconsScanArgs struct{}

func (SourceIconsScanArgs) Kind() string { return "myhot_source_icons_scan" }

type MPScanArgs struct{}

func (MPScanArgs) Kind() string { return "myhot_mp_account_scan" }

type MPCheckArgs struct {
	SourceID string `json:"sourceId"`
}

func (MPCheckArgs) Kind() string { return "myhot_mp_account_check" }

type SourceIconsArgs struct {
	Day string `json:"day" river:"unique"`
}

func (SourceIconsArgs) Kind() string { return "myhot_source_icons" }

type SourceArgs struct {
	SourceID string `json:"sourceId" river:"unique"`
	Force    bool   `json:"force,omitempty"`
}

func (SourceArgs) Kind() string { return "myhot_collect_source" }

type XShardArgs struct {
	Key       string   `json:"key"`
	SourceIDs []string `json:"sourceIds"`
}

func (XShardArgs) Kind() string { return "myhot_collect_x_shard" }

type SourceScanWorker struct {
	river.WorkerDefaults[SourceScanArgs]
	Service *collector.Service
	Client  *river.Client[pgx.Tx]
	Enabled bool
}
type SourceWorker struct {
	river.WorkerDefaults[SourceArgs]
	Service *collector.Service
	Enabled bool
}
type XShardWorker struct {
	river.WorkerDefaults[XShardArgs]
	Service *collector.Service
	Enabled bool
}

type SourceIconsScanWorker struct {
	river.WorkerDefaults[SourceIconsScanArgs]
	Service *sourceicons.Service
	Client  *river.Client[pgx.Tx]
}

type SourceIconsWorker struct {
	river.WorkerDefaults[SourceIconsArgs]
	Service *sourceicons.Service
	DB      *gorm.DB
}

type MPScanWorker struct {
	river.WorkerDefaults[MPScanArgs]
	Service interface {
		DueSourceIDs(context.Context) ([]string, error)
	}
	Client  *river.Client[pgx.Tx]
	DB      *gorm.DB
	Enabled bool
}

type MPCheckWorker struct {
	river.WorkerDefaults[MPCheckArgs]
	Service interface {
		CheckAccount(context.Context, string) error
	}
	Enabled bool
}

type mpAccountSchedule struct{ location *time.Location }

func (s mpAccountSchedule) Next(current time.Time) time.Time {
	now := current.In(s.location)
	minute := (now.Minute()/15 + 1) * 15
	next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, s.location).Add(time.Duration(minute) * time.Minute)
	return next
}

func newMPAccountSchedule() (mpAccountSchedule, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return mpAccountSchedule{}, err
	}
	return mpAccountSchedule{location: location}, nil
}

func mpAccountPeriodicOptions() *river.PeriodicJobOpts {
	return &river.PeriodicJobOpts{ID: "myhot-mp-account-scan"}
}

func sourceInsertOpts() *river.InsertOpts {
	// Collection repeats after completion; only active jobs for this source conflict.
	return &river.InsertOpts{
		Queue:       "rss_sources",
		Priority:    2,
		MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			}}}
}

func ManualSourceFetchArgs(sourceID, sourceKind string) (river.JobArgs, *river.InsertOpts, error) {
	if sourceKind == "mp_account" {
		return MPCheckArgs{SourceID: sourceID},
			&river.InsertOpts{
				Queue:       "mp_accounts",
				Priority:    2,
				MaxAttempts: 4,
				UniqueOpts: river.UniqueOpts{
					ByArgs: true,
					ByState: []rivertype.JobState{
						rivertype.JobStateAvailable,
						rivertype.JobStatePending,
						rivertype.JobStateRunning,
						rivertype.JobStateRetryable,
						rivertype.JobStateScheduled,
					}}}, nil
	}
	if sourceKind == "rss" || sourceKind == "web_list" || sourceKind == "json_list" || sourceKind == "x_search" {
		options := *sourceInsertOpts()
		options.MaxAttempts = 1
		return SourceArgs{SourceID: sourceID, Force: true}, &options, nil
	}
	return nil, nil, fmt.Errorf("manual fetch is not available for source kind %q", sourceKind)
}

func InsertManualSourceFetch(ctx context.Context, pool *pgxpool.Pool, sourceID, sourceKind string) (int64, error) {
	args, options, err := ManualSourceFetchArgs(sourceID, sourceKind)
	if err != nil {
		return 0, err
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return 0, err
	}
	result, err := client.Insert(ctx, args, options)
	if err != nil {
		return 0, err
	}
	if result.Job == nil {
		return 0, errors.New("River did not return a source job")
	}
	return result.Job.ID, nil
}

// Work 扫描任务执行，查询到期信源
func (w *SourceScanWorker) Work(ctx context.Context, _ *river.Job[SourceScanArgs]) error {
	if !w.Enabled {
		return nil
	}

	// 查询DB，筛选需要执行抓取的id
	ids, err := w.Service.DueSourceIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		// 把到期信源加入Queue
		if _, err := w.Client.Insert(
			ctx,
			SourceArgs{SourceID: id},
			sourceInsertOpts()); err != nil {
			return err
		}
	}
	shards, err := w.Service.DueXShards(ctx)
	if err != nil {
		return err
	}
	for _, shard := range shards {
		_, err := w.Client.Insert(ctx, XShardArgs{Key: shard.Key, SourceIDs: shard.SourceIDs}, &river.InsertOpts{
			Queue: "x_search", Priority: 2, MaxAttempts: 3,
			UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			}},
		})
		if err != nil {
			return err
		}
		if err := w.Service.DeferXSources(ctx, shard.SourceIDs); err != nil {
			return err
		}
	}
	return nil
}
func (w *SourceWorker) Work(ctx context.Context, job *river.Job[SourceArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	if job.Args.Force {
		return finishSourceWork(w.Service.CollectSourceNow(ctx, job.Args.SourceID))
	}

	// 自主抓取
	return finishSourceWork(w.Service.CollectSource(ctx, job.Args.SourceID))
}

func (w *XShardWorker) Work(ctx context.Context, job *river.Job[XShardArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	err := w.Service.CollectXShard(ctx, job.Args.Key, job.Args.SourceIDs)
	if errors.Is(err, collector.ErrFetchRecorded) {
		return nil
	}
	return err
}

func (w *XShardWorker) Timeout(*river.Job[XShardArgs]) time.Duration { return 11 * time.Minute }

func (w *MPScanWorker) Work(ctx context.Context, _ *river.Job[MPScanArgs]) error {
	return runTrackedTask(ctx, w.DB, "sources.mp-reconcile", func() (any, error) {
		if !w.Enabled {
			return map[string]int{"enqueued": 0}, nil
		}
		ids, err := w.Service.DueSourceIDs(ctx)
		if err != nil {
			return nil, err
		}
		enqueued := 0
		for _, id := range ids {
			result, err := w.Client.Insert(ctx, MPCheckArgs{SourceID: id}, &river.InsertOpts{Queue: "mp_accounts", Priority: 2, MaxAttempts: 4, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			}}})
			if err != nil {
				return nil, err
			}
			if !result.UniqueSkippedAsDuplicate {
				enqueued++
			}
		}
		return map[string]int{"enqueued": enqueued}, nil
	})
}

func (w *MPCheckWorker) Work(ctx context.Context, job *river.Job[MPCheckArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	err := w.Service.CheckAccount(ctx, job.Args.SourceID)
	if errors.Is(err, mpcollector.ErrFetchRecorded) {
		return nil
	}
	return err
}

func (w *MPCheckWorker) Timeout(*river.Job[MPCheckArgs]) time.Duration {
	return 4*time.Minute + 30*time.Second
}

func finishSourceWork(err error) error {
	if errors.Is(err, collector.ErrFetchRecorded) {
		return nil
	}
	return err
}

func (w *SourceIconsScanWorker) Work(ctx context.Context, _ *river.Job[SourceIconsScanArgs]) error {
	day, due, err := w.Service.Due(ctx, time.Now())
	if err != nil || !due {
		return err
	}
	_, err = w.Client.Insert(ctx, SourceIconsArgs{Day: day}, &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return err
}

func (w *SourceIconsWorker) Work(ctx context.Context, _ *river.Job[SourceIconsArgs]) error {
	return runTrackedTask(ctx, w.DB, "sources.icons", func() (any, error) {
		return w.Service.Run(ctx)
	})
}

func (w *SourceIconsWorker) Timeout(*river.Job[SourceIconsArgs]) time.Duration { return 6 * time.Hour }

func (w *SourceWorker) Timeout(*river.Job[SourceArgs]) time.Duration { return 11 * time.Minute }
