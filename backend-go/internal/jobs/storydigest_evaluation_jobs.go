package jobs

import (
	"context"
	"time"

	"aihot/backend-go/internal/receipts"
	"aihot/backend-go/internal/storydigest"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

type StoryDigestEvaluationArgs struct {
	Options       storydigest.EvaluationOptions `json:"options"`
	CasesFile     string                        `json:"casesFile"`
	CandidateFile string                        `json:"candidateFile,omitempty"`
}

/** Kind identifies an event digest evaluation. */
func (StoryDigestEvaluationArgs) Kind() string {
	return "eval.story_digests"
}

/** InsertOpts uses the isolated evaluation queue. */
func (StoryDigestEvaluationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "evaluations", MaxAttempts: 2}
}

type StoryDigestEvaluationWorker struct {
	river.WorkerDefaults[StoryDigestEvaluationArgs]
	Service *storydigest.EvaluationService
	Models  interface {
		ResolveKey(context.Context, string) (string, error)
	}
	Usage   storydigest.EvaluationUsageRepository
	DB      *gorm.DB
	Enabled bool
}

/** Work records a digest comparison without modifying the source stories. */
func (w *StoryDigestEvaluationWorker) Work(ctx context.Context, job *river.Job[StoryDigestEvaluationArgs]) error {
	if !w.Enabled {
		return river.JobSnooze(5 * time.Minute)
	}
	return runTrackedTask(ctx, w.DB, "eval.story_digests", func() (any, error) {
		options := job.Args.Options
		if len(options.Models) == 0 {
			key, err := w.Models.ResolveKey(ctx, "digest")
			if err != nil {
				return nil, err
			}
			options.Models = []string{key}
		}
		created := time.Now()
		run, err := w.Service.Run(ctx, options)
		if err != nil {
			return nil, err
		}
		report, err := storydigest.BuildEvaluationReport(ctx, w.Usage, run, job.Args.CasesFile, job.Args.CandidateFile, created)
		if err != nil {
			return nil, err
		}
		return struct {
			JobID  int64                                  `json:"jobId"`
			Report storydigest.EvaluationComparisonReport `json:"report"`
		}{JobID: job.ID, Report: report}, nil
	})
}

/** Timeout allows bounded comparisons to finish while respecting River recovery. */
func (w *StoryDigestEvaluationWorker) Timeout(*river.Job[StoryDigestEvaluationArgs]) time.Duration {
	return 30 * time.Minute
}

var _ storydigest.EvaluationUsageRepository = (*receipts.GormRepository)(nil)
