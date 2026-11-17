package jobs

import (
	"context"
	"errors"
	"time"

	"aihot/backend-go/internal/grouping"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

type RelationEvaluationArgs struct {
	Options grouping.RelationEvaluationOptions `json:"options"`
}

/** Kind identifies a relation evaluation task. */
func (RelationEvaluationArgs) Kind() string {
	return "eval.relations"
}

/** InsertOpts isolates paid evaluations from normal user jobs. */
func (RelationEvaluationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       "evaluations",
		MaxAttempts: 2,
	}
}

type RelationEvaluationWorker struct {
	river.WorkerDefaults[RelationEvaluationArgs]
	Service *grouping.RelationEvaluationService
	Models  interface {
		ResolveKey(context.Context, string) (string, error)
	}
	DB *gorm.DB
}

/** Work records the evaluation report in the existing task audit. */
func (w *RelationEvaluationWorker) Work(ctx context.Context, job *river.Job[RelationEvaluationArgs]) error {
	return runTrackedTask(ctx, w.DB, "eval.relations", func() (any, error) {
		options, err := w.resolveOptions(ctx, job.Args.Options)
		if err != nil {
			return nil, err
		}
		report, err := w.Service.Run(ctx, options)
		if err != nil {
			return nil, err
		}
		return struct {
			JobID  int64                          `json:"jobId"`
			Report grouping.RelationEvaluationRun `json:"report"`
		}{
			JobID:  job.ID,
			Report: report,
		}, nil
	})
}

/** resolveOptions uses the configured review model when no model was requested. */
func (w *RelationEvaluationWorker) resolveOptions(ctx context.Context, options grouping.RelationEvaluationOptions) (grouping.RelationEvaluationOptions, error) {
	if len(options.Models) != 0 {
		return options, nil
	}
	if w.Models == nil {
		return options, errors.New("evaluation model resolver is unavailable")
	}
	key, err := w.Models.ResolveKey(ctx, "groupReview")
	if err != nil {
		return options, err
	}
	options.Models = []string{key}
	return options, nil
}

/** Timeout allows a multi-model evaluation to finish within the task deadline. */
func (w *RelationEvaluationWorker) Timeout(*river.Job[RelationEvaluationArgs]) time.Duration {
	return 30 * time.Minute
}
