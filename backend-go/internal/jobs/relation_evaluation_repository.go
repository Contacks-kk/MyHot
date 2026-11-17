package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"aihot/backend-go/internal/grouping"
	"aihot/backend-go/internal/storydigest"
	"github.com/jackc/pgx/v5/pgxpool"
)

/** SelectionEvaluationReport loads the completed SelectBench task and report from job audit. */
func SelectionEvaluationReport(ctx context.Context, pool *pgxpool.Pool, id int64) (SelectionEvaluationTaskResult, error) {
	var result SelectionEvaluationTaskResult
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT detail FROM job_runs WHERE job = 'eval.selection' AND status = 'ok' AND detail->>'jobId' = $1 ORDER BY id DESC LIMIT 1`, strconv.FormatInt(id, 10)).Scan(&raw)
	if err != nil {
		return result, fmt.Errorf("load evaluation audit: %w", err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("decode evaluation report: %w", err)
	}
	return result, nil
}

/** RelationEvaluationReport loads a completed report from the task audit. */
func RelationEvaluationReport(ctx context.Context, pool *pgxpool.Pool, id int64) (grouping.RelationEvaluationRun, error) {
	var report grouping.RelationEvaluationRun
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT detail->'report' FROM job_runs WHERE job = 'eval.relations' AND status = 'ok' AND detail->>'jobId' = $1 ORDER BY id DESC LIMIT 1`, strconv.FormatInt(id, 10)).Scan(&raw)
	if err != nil {
		return report, fmt.Errorf("load evaluation audit: %w", err)
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, fmt.Errorf("decode evaluation report: %w", err)
	}
	return report, nil
}

/** StoryDigestEvaluationReport loads a completed digest comparison from the task audit. */
func StoryDigestEvaluationReport(ctx context.Context, pool *pgxpool.Pool, id int64) (storydigest.EvaluationComparisonReport, error) {
	var report storydigest.EvaluationComparisonReport
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT detail->'report' FROM job_runs WHERE job = 'eval.story_digests' AND status = 'ok' AND detail->>'jobId' = $1 ORDER BY id DESC LIMIT 1`, strconv.FormatInt(id, 10)).Scan(&raw)
	if err != nil {
		return report, fmt.Errorf("load evaluation audit: %w", err)
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, fmt.Errorf("decode evaluation report: %w", err)
	}
	return report, nil
}
