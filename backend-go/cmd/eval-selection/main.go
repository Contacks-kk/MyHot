package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"aihot/backend-go/internal/adminmodels"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

/** main submits a selection evaluation or resumes its persisted task. */
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

/** run parses options, waits for the worker, and writes the SelectBench report. */
func run() error {
	goldPath := flag.String("gold", "", "labelled JSONL input (defaults to $AIHOT_DATA_DIR/gold.jsonl)")
	modelList := flag.String("models", "", "comma-separated model keys")
	limit := flag.Int("n", 200, "maximum number of cases")
	split := flag.String("split", "all", "benchmark split")
	concurrency := flag.Int("concurrency", 6, "maximum concurrent model calls")
	seed := flag.Int64("seed", 7, "sampling seed")
	sweepStart := flag.Int("sweep-start", 40, "threshold sweep start")
	sweepEnd := flag.Int("sweep-end", 90, "threshold sweep end")
	label := flag.String("label", "", "SelectBench label")
	noImport := flag.Bool("no-import", false, "skip automatic SelectBench import")
	jobID := flag.Int64("job", 0, "resume an existing task")
	flag.Parse()
	if flag.NArg() != 0 || *jobID < 0 {
		return errors.New("invalid command arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var options editorial.SelectionEvaluationOptions
	if *jobID == 0 {
		if !cfg.ModelCallsEnabled {
			return errors.New("MODEL_CALLS_ENABLED must be true to start a selection evaluation")
		}
		catalog, err := adminmodels.LoadCatalog()
		if err != nil {
			return err
		}
		path := *goldPath
		if path == "" {
			path = filepath.Join(cfg.DataDir, "gold.jsonl")
		}
		allowedModels := append([]string{"default"}, catalog.PresetOrder...)
		options, err = readOptions(path, *modelList, allowedModels, *limit, *split, *concurrency, *seed, *sweepStart, *sweepEnd, *label, *noImport)
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return err
	}
	if *jobID == 0 {
		inserted, err := client.Insert(ctx, jobs.SelectionEvaluationArgs{Options: options}, nil)
		if err != nil {
			return err
		}
		*jobID = inserted.Job.ID
		fmt.Fprintf(os.Stderr, "evaluation job: %d\n", *jobID)
	}
	result, err := waitForReport(ctx, client, pool, *jobID)
	if err != nil {
		return err
	}
	if err := writeReport(cfg.DataDir, result.Report); err != nil {
		return err
	}
	if result.SelectBenchRun != "" {
		fmt.Printf("SelectBench run: %s\n", result.SelectBenchRun)
	}
	return nil
}

/** readOptions validates the gold file and task arguments before queue insertion. */
func readOptions(path, models string, allowedModels []string, limit int, split string, concurrency int, seed int64, sweepStart int, sweepEnd int, label string, noImport bool) (editorial.SelectionEvaluationOptions, error) {
	if limit <= 0 || concurrency <= 0 {
		return editorial.SelectionEvaluationOptions{}, errors.New("n and concurrency must be positive")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return editorial.SelectionEvaluationOptions{}, err
	}
	if _, err := editorial.ParseSelectionGold(string(data)); err != nil {
		return editorial.SelectionEvaluationOptions{}, err
	}
	options := editorial.SelectionEvaluationOptions{
		Gold:        string(data),
		Limit:       limit,
		Split:       split,
		Concurrency: concurrency,
		Seed:        seed,
		SweepStart:  sweepStart,
		SweepEnd:    sweepEnd,
		Label:       label,
		NoImport:    noImport,
	}
	for _, value := range strings.Split(models, ",") {
		if model := strings.TrimSpace(value); model != "" {
			options.Models = append(options.Models, model)
		}
	}
	if models != "" && len(options.Models) == 0 {
		return editorial.SelectionEvaluationOptions{}, errors.New("models did not name any models")
	}
	allowed := make(map[string]struct{}, len(allowedModels))
	for _, model := range allowedModels {
		allowed[model] = struct{}{}
	}
	for _, model := range options.Models {
		if _, ok := allowed[model]; !ok {
			return editorial.SelectionEvaluationOptions{}, fmt.Errorf("unknown model %s", model)
		}
	}
	return options, nil
}

/** waitForReport polls the persisted River task and reads its success audit. */
func waitForReport(ctx context.Context, client *river.Client[pgx.Tx], pool *pgxpool.Pool, id int64) (jobs.SelectionEvaluationTaskResult, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		job, err := client.JobGet(ctx, id)
		if err != nil {
			if errors.Is(err, river.ErrNotFound) {
				return jobs.SelectionEvaluationReport(ctx, pool, id)
			}
			return jobs.SelectionEvaluationTaskResult{}, err
		}
		if job.Kind != (jobs.SelectionEvaluationArgs{}).Kind() {
			return jobs.SelectionEvaluationTaskResult{}, errors.New("job is not a selection evaluation")
		}
		switch job.State {
		case rivertype.JobStateCompleted:
			return jobs.SelectionEvaluationReport(ctx, pool, id)
		case rivertype.JobStateCancelled, rivertype.JobStateDiscarded:
			if len(job.Errors) > 0 {
				return jobs.SelectionEvaluationTaskResult{}, fmt.Errorf("evaluation job %d %s: %s", id, job.State, job.Errors[len(job.Errors)-1].Error)
			}
			return jobs.SelectionEvaluationTaskResult{}, fmt.Errorf("evaluation job %d %s", id, job.State)
		}
		select {
		case <-ctx.Done():
			return jobs.SelectionEvaluationTaskResult{}, fmt.Errorf("evaluation job %d remains in the queue: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

/** writeReport stores the report under the configured data directory. */
func writeReport(dataDir string, report editorial.SelectionEvaluationRun) error {
	directory := filepath.Join(dataDir, "eval")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	filename := editorial.SelectionEvaluationReportFileName(report.Meta.Split, report.Meta.N, time.Now().UnixMilli())
	path := filepath.Join(directory, filename)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return err
	}
	fmt.Printf("report: %s\n", path)
	return nil
}
