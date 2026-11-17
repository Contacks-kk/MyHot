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

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/jobs"
	"aihot/backend-go/internal/storydigest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

/** main submits or retrieves an evaluation without calling a model. */
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

/** run reads evaluation arguments and waits for the worker's report. */
func run() error {
	stories := flag.String("stories", "", "comma-separated public event IDs to export")
	out := flag.String("out", "", "snapshot JSONL output path")
	casesFile := flag.String("cases", ".data/story-digest-cases.jsonl", "labelled JSONL cases")
	systemFile := flag.String("system", "", "candidate prompt file")
	models := flag.String("models", "", "comma-separated model keys")
	maxCalls := flag.Int("max-calls", 18, "maximum model calls")
	concurrency := flag.Int("concurrency", 4, "maximum concurrent calls")
	outDir := flag.String("out-dir", ".data/eval", "report directory")
	jobID := flag.Int64("job", 0, "resume an existing job")
	flag.Parse()
	if flag.NArg() != 0 || *jobID < 0 {
		return errors.New("invalid command arguments")
	}
	storiesProvided := false
	flag.Visit(func(item *flag.Flag) {
		if item.Name == "stories" {
			storiesProvided = true
		}
	})
	if storiesProvided {
		if *out == "" || *jobID != 0 {
			return errors.New("--stories requires --out and cannot be combined with --job")
		}
		ids, err := storydigest.ParseEvaluationStoryIDs(*stories)
		if err != nil {
			return err
		}
		return exportStories(ids, *out)
	}
	var args jobs.StoryDigestEvaluationArgs
	if *jobID == 0 {
		var err error
		args, err = readOptions(*casesFile, *systemFile, *models, *maxCalls, *concurrency)
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
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
		inserted, err := client.Insert(ctx, args, nil)
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
	return writeReports(*outDir, report)
}

/** exportStories writes a complete read-only snapshot without making model calls. */
func exportStories(ids []string, output string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	orm, err := db.OpenGORM(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.CloseGORM(orm)
	cases, err := storydigest.NewRepository(orm).ExportEvaluationCases(ctx, ids, time.Now())
	if err != nil {
		return err
	}
	var content strings.Builder
	encoder := json.NewEncoder(&content)
	encoder.SetEscapeHTML(false)
	for _, row := range cases {
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	directory := filepath.Dir(output)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".story-digests-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := file.WriteString(content.String()); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, output); err != nil {
		return err
	}
	fmt.Printf("snapshot: %s\n", output)
	return nil
}

/** readOptions validates and snapshots the input files before enqueueing. */
func readOptions(casesFile, systemFile, models string, maxCalls, concurrency int) (jobs.StoryDigestEvaluationArgs, error) {
	if maxCalls <= 0 || concurrency <= 0 {
		return jobs.StoryDigestEvaluationArgs{}, errors.New("max-calls and concurrency must be positive")
	}
	data, err := os.ReadFile(casesFile)
	if err != nil {
		return jobs.StoryDigestEvaluationArgs{}, err
	}
	rows, err := storydigest.ParseEvaluationCases(string(data))
	if err != nil {
		return jobs.StoryDigestEvaluationArgs{}, err
	}
	if len(rows) == 0 {
		return jobs.StoryDigestEvaluationArgs{}, errors.New("no digest evaluation cases")
	}
	args := jobs.StoryDigestEvaluationArgs{
		CasesFile:     casesFile,
		CandidateFile: systemFile,
		Options:       storydigest.EvaluationOptions{Cases: string(data), MaxCalls: maxCalls, Concurrency: concurrency},
	}
	for _, model := range strings.Split(models, ",") {
		if model = strings.TrimSpace(model); model != "" {
			args.Options.Models = append(args.Options.Models, model)
		}
	}
	if models != "" && len(args.Options.Models) == 0 {
		return args, errors.New("models did not name any models")
	}
	promptCount := 1
	if systemFile != "" {
		data, err := os.ReadFile(systemFile)
		if err != nil {
			return args, err
		}
		candidate := string(data)
		args.Options.Candidate = &candidate
		promptCount++
	}
	modelCount := max(1, len(args.Options.Models))
	if len(rows) > maxCalls/modelCount/promptCount {
		return args, errors.New("evaluation exceeds the model call limit")
	}
	fmt.Fprintf(os.Stderr, "calls: %d = %d case(s) × %d model(s) × %d prompt(s), limit %d\n", len(rows)*modelCount*promptCount, len(rows), modelCount, promptCount, maxCalls)
	return args, nil
}

/** waitForReport follows the job and retrieves its persisted audit report. */
func waitForReport(ctx context.Context, client *river.Client[pgx.Tx], pool *pgxpool.Pool, id int64) (storydigest.EvaluationComparisonReport, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		job, err := client.JobGet(ctx, id)
		if errors.Is(err, river.ErrNotFound) {
			return jobs.StoryDigestEvaluationReport(ctx, pool, id)
		}
		if err != nil {
			return storydigest.EvaluationComparisonReport{}, err
		}
		if job.Kind != (jobs.StoryDigestEvaluationArgs{}).Kind() {
			return storydigest.EvaluationComparisonReport{}, errors.New("job is not a story digest evaluation")
		}
		switch job.State {
		case rivertype.JobStateCompleted:
			return jobs.StoryDigestEvaluationReport(ctx, pool, id)
		case rivertype.JobStateCancelled, rivertype.JobStateDiscarded:
			if len(job.Errors) > 0 {
				return storydigest.EvaluationComparisonReport{}, fmt.Errorf("evaluation job %d %s: %s", id, job.State, job.Errors[len(job.Errors)-1].Error)
			}
			return storydigest.EvaluationComparisonReport{}, fmt.Errorf("evaluation job %d %s", id, job.State)
		}
		select {
		case <-ctx.Done():
			return storydigest.EvaluationComparisonReport{}, fmt.Errorf("evaluation job %d remains in the queue: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

/** writeReports saves both machine-readable and human-readable comparisons. */
func writeReports(directory string, report storydigest.EvaluationComparisonReport) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	base := filepath.Join(directory, fmt.Sprintf("story-digests-%d", time.Now().UnixMilli()))
	if err := os.WriteFile(base+".json", data, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(base+".md", []byte(storydigest.EvaluationMarkdown(report)), 0o600); err != nil {
		return err
	}
	fmt.Printf("report: %s.json\nmarkdown: %s.md\n", base, base)
	return nil
}
