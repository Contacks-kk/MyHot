package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/grouping"
	"aihot/backend-go/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

/** main submits or retrieves an evaluation without making model calls. */
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

/** run parses the management command and waits for the worker report. */
func run() error {
	goldPath := flag.String("gold", ".data/relation-gold.jsonl", "labelled JSONL input")
	modelList := flag.String("models", "", "comma-separated model keys")
	limit := flag.Int("n", 200, "maximum number of cases")
	split := flag.String("split", "all", "benchmark split")
	concurrency := flag.Int("concurrency", 6, "maximum concurrent model calls")
	seed := flag.Int64("seed", 7, "sampling seed")
	thresholdList := flag.String("thresholds", "0.75,0.8", "comma-separated story thresholds")
	jobID := flag.Int64("job", 0, "resume waiting for an existing job")
	flag.Parse()
	if flag.NArg() != 0 || *jobID < 0 {
		return errors.New("invalid command arguments")
	}
	var options grouping.RelationEvaluationOptions
	if *jobID == 0 {
		var err error
		options, err = parseOptions(*goldPath, *modelList, *split, *limit, *concurrency, *seed, *thresholdList)
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *jobID == 0 && !cfg.ModelCallsEnabled {
		return errors.New("MODEL_CALLS_ENABLED must be true to start a relation evaluation")
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
		inserted, err := client.Insert(ctx, jobs.RelationEvaluationArgs{Options: options}, nil)
		if err != nil {
			return err
		}
		*jobID = inserted.Job.ID
		fmt.Fprintf(os.Stderr, "evaluation job: %d\n", *jobID)
	}
	report, err := waitForReport(ctx, client, pool, *jobID)
	if err != nil {
		return err
	}
	return writeReport(cfg.DataDir, report)
}

/** parseOptions checks input before persisting the worker task. */
func parseOptions(path, models, split string, limit, concurrency int, seed int64, thresholds string) (grouping.RelationEvaluationOptions, error) {
	if limit <= 0 || concurrency <= 0 {
		return grouping.RelationEvaluationOptions{}, errors.New("n and concurrency must be positive")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return grouping.RelationEvaluationOptions{}, err
	}
	rows, err := grouping.ParseRelationGoldJSONL(string(data))
	if err != nil {
		return grouping.RelationEvaluationOptions{}, err
	}
	if len(grouping.SampleRelationGold(rows, split, limit, uint32(seed))) == 0 {
		return grouping.RelationEvaluationOptions{}, fmt.Errorf("no cases for split %s", split)
	}
	options := grouping.RelationEvaluationOptions{
		Gold:        string(data),
		Split:       split,
		Limit:       limit,
		Concurrency: concurrency,
		Seed:        seed,
	}
	for _, value := range strings.Split(models, ",") {
		if name := strings.TrimSpace(value); name != "" {
			options.Models = append(options.Models, name)
		}
	}
	if models != "" && len(options.Models) == 0 {
		return grouping.RelationEvaluationOptions{}, errors.New("models did not name any models")
	}
	for _, value := range strings.Split(thresholds, ",") {
		threshold, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
			return grouping.RelationEvaluationOptions{}, errors.New("thresholds must be numbers between zero and one")
		}
		options.Thresholds = append(options.Thresholds, threshold)
	}
	return options, nil
}

/** waitForReport follows the persisted task and reads its completed audit entry. */
func waitForReport(ctx context.Context, client *river.Client[pgx.Tx], pool *pgxpool.Pool, id int64) (grouping.RelationEvaluationRun, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		job, err := client.JobGet(ctx, id)
		if err != nil {
			if errors.Is(err, river.ErrNotFound) {
				return jobs.RelationEvaluationReport(ctx, pool, id)
			}
			return grouping.RelationEvaluationRun{}, err
		}
		if job.Kind != (jobs.RelationEvaluationArgs{}).Kind() {
			return grouping.RelationEvaluationRun{}, errors.New("job is not a relation evaluation")
		}
		switch job.State {
		case rivertype.JobStateCompleted:
			return jobs.RelationEvaluationReport(ctx, pool, id)
		case rivertype.JobStateCancelled, rivertype.JobStateDiscarded:
			if len(job.Errors) > 0 {
				return grouping.RelationEvaluationRun{}, fmt.Errorf("evaluation job %d %s: %s", id, job.State, job.Errors[len(job.Errors)-1].Error)
			}
			return grouping.RelationEvaluationRun{}, fmt.Errorf("evaluation job %d %s", id, job.State)
		}
		select {
		case <-ctx.Done():
			return grouping.RelationEvaluationRun{}, fmt.Errorf("evaluation job %d remains in the queue: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

/** writeReport saves the legacy-compatible report in the configured data directory. */
func writeReport(dataDir string, report grouping.RelationEvaluationRun) error {
	directory := filepath.Join(dataDir, "eval")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	name := grouping.SafeEvaluationReportNamePart(report.Meta.Split)
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(directory, fmt.Sprintf("relations-%s-%d-%d.json", name, report.Meta.N, time.Now().UnixMilli()))
	if err := os.WriteFile(file, encoded, 0o600); err != nil {
		return err
	}
	fmt.Printf("report: %s\n", file)
	return nil
}
