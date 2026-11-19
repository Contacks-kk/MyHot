package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aihot/backend-go/internal/editorial"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

type SelectionEvaluationArgs struct {
	Options editorial.SelectionEvaluationOptions `json:"options"`
}

/** Kind identifies a selection benchmark evaluation. */
func (SelectionEvaluationArgs) Kind() string {
	return "eval.selection"
}

/** InsertOpts keeps paid model evaluations on the isolated queue. */
func (SelectionEvaluationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "evaluations", MaxAttempts: 2}
}

type SelectionEvaluationWorker struct {
	river.WorkerDefaults[SelectionEvaluationArgs]
	Service *editorial.SelectionEvaluationService
	Models  interface {
		ResolveKey(context.Context, string) (string, error)
	}
	Importer interface {
		ImportWithID(context.Context, string, string, json.RawMessage, string) (string, error)
	}
	DB      *gorm.DB
	Enabled bool
}

type SelectionEvaluationTaskResult struct {
	JobID          int64                            `json:"jobId"`
	SelectBenchRun string                           `json:"selectbenchRun,omitempty"`
	Report         editorial.SelectionEvaluationRun `json:"report"`
}

/** Work evaluates the gold set and records its report and optional SelectBench import. */
func (w *SelectionEvaluationWorker) Work(ctx context.Context, job *river.Job[SelectionEvaluationArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	return runTrackedTask(ctx, w.DB, "eval.selection", func() (any, error) {
		options, err := w.resolveOptions(ctx, job.Args.Options)
		if err != nil {
			return nil, err
		}
		report, err := w.Service.Run(ctx, options)
		if err != nil {
			return nil, err
		}
		result := SelectionEvaluationTaskResult{JobID: job.ID, Report: report}
		if !options.NoImport {
			if w.Importer == nil {
				return nil, errors.New("selectbench import is unavailable")
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				return nil, err
			}
			label := options.Label
			if label == "" {
				label = fmt.Sprintf("%s %d 条 · %s", options.Split, report.Meta.N, joinEvaluationModels(report))
			}
			id := fmt.Sprintf("sb-eval-%d", job.ID)
			result.SelectBenchRun, err = w.Importer.ImportWithID(ctx, id, label, encoded, "script:eval-selection")
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	})
}

/** resolveOptions selects the currently configured score model when none was supplied. */
func (w *SelectionEvaluationWorker) resolveOptions(ctx context.Context, options editorial.SelectionEvaluationOptions) (editorial.SelectionEvaluationOptions, error) {
	if len(options.Models) != 0 {
		return options, nil
	}
	if w.Models == nil {
		return options, fmt.Errorf("evaluation model resolver is unavailable")
	}
	key, err := w.Models.ResolveKey(ctx, "score")
	if err != nil {
		return options, err
	}
	options.Models = []string{key}
	return options, nil
}

/** joinEvaluationModels formats models in the order requested for the default label. */
func joinEvaluationModels(report editorial.SelectionEvaluationRun) string {
	return strings.Join(report.ModelNames(), " / ")
}

/** Timeout allows bounded multi-model evaluations to finish on the dedicated queue. */
func (w *SelectionEvaluationWorker) Timeout(*river.Job[SelectionEvaluationArgs]) time.Duration {
	return 30 * time.Minute
}
