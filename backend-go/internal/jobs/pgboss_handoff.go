package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

const pgBossHandoffBatchSize = 100

type PgBossHandoffResult struct {
	Imported int `json:"imported"`
	Retired  int `json:"retired"`
}

type pgBossPendingJob struct {
	ID         string
	Name       string
	SourceKind string
	Data       []byte
	State      string
	StartAfter time.Time
	RetryCount int
}

// ImportPendingPgBossJobs transfers only the queue work owned by the Go shared pipeline.
// The pg-boss row and River insert are committed in the same PostgreSQL transaction, so a
// crash cannot acknowledge the source row without also preserving its work in River.
func ImportPendingPgBossJobs(ctx context.Context, pool *pgxpool.Pool, client *river.Client[pgx.Tx], collectorEnabled, manualMPEnabled, digestEnabled bool) (PgBossHandoffResult, error) {
	var result PgBossHandoffResult
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('pgboss.job') IS NOT NULL`).Scan(&exists); err != nil {
		return result, err
	}
	if !exists {
		return result, nil
	}
	for {
		batch, err := importPgBossBatch(ctx, pool, client, "pgboss", collectorEnabled, manualMPEnabled, digestEnabled)
		if err != nil {
			return result, err
		}
		result.Imported += batch.Imported
		result.Retired += batch.Retired
		if batch.Imported+batch.Retired < pgBossHandoffBatchSize {
			return result, nil
		}
	}
}

// importPgBossBatch keeps the schema argument identifier-safe so integration tests can use an
// isolated schema without touching a developer's actual pgboss queue.
func importPgBossBatch(ctx context.Context, pool *pgxpool.Pool, client *river.Client[pgx.Tx], schema string, collectorEnabled, manualMPEnabled, digestEnabled bool) (PgBossHandoffResult, error) {
	var result PgBossHandoffResult
	if schema == "" {
		return result, errors.New("pg-boss schema is empty")
	}
	table := pgx.Identifier{schema, "job"}.Sanitize()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queueNames := []string{"sources.fetch", "sources.fetch-x", "sources.mp", "notify.selected", "media.prepare", "content.analyze", "content.extract-body", "events.group", "publication.republish-source"}
	if digestEnabled {
		queueNames = append(queueNames, "events.digest")
	}
	releasedQueueNames := []string{"content.analyze", "content.extract-body", "events.group"}
	if digestEnabled {
		releasedQueueNames = append(releasedQueueNames, "events.digest")
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT j.id::text,j.name,j.data,j.state,j.start_after,j.retry_count,
		CASE WHEN j.name='sources.fetch' THEN coalesce((SELECT s.kind FROM sources s WHERE s.id=j.data->>'sourceId'),'') ELSE '' END
		FROM %s j WHERE j.name = ANY($1::text[]) AND (j.state IN ('created','retry') OR
			(j.state='failed' AND j.name=ANY($4::text[]) AND EXISTS (
				SELECT 1 FROM audit_log a WHERE a.action='receipt.release'
				AND a.subject='receipt:' || (j.output->>'receiptId') AND a.created_at>j.started_on)))
		AND ($3::boolean OR j.name <> 'sources.mp' OR j.data->>'reason' IS DISTINCT FROM 'manual')
		AND ($5::boolean OR j.name <> 'sources.fetch' OR j.data->>'force' IS DISTINCT FROM 'true')
		ORDER BY j.created_on,j.id FOR UPDATE OF j SKIP LOCKED LIMIT $2`, table),
		queueNames, pgBossHandoffBatchSize, manualMPEnabled, releasedQueueNames, collectorEnabled)
	if err != nil {
		return result, err
	}
	var pending []pgBossPendingJob
	for rows.Next() {
		var job pgBossPendingJob
		if err := rows.Scan(&job.ID, &job.Name, &job.Data, &job.State, &job.StartAfter, &job.RetryCount, &job.SourceKind); err != nil {
			rows.Close()
			return result, err
		}
		pending = append(pending, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	for _, job := range pending {
		args, opts, transfer, err := pgBossRiverJob(job)
		if err != nil {
			return result, fmt.Errorf("decode pg-boss job %s (%s): %w", job.ID, job.Name, err)
		}
		var riverJobID any
		if transfer {
			inserted, err := client.InsertTx(ctx, tx, args, opts)
			if err != nil {
				return result, fmt.Errorf("insert River job for pg-boss job %s: %w", job.ID, err)
			}
			if inserted.Job != nil {
				riverJobID = inserted.Job.ID
			}
			result.Imported++
		} else {
			if err := clearRetiredQueueStamp(ctx, tx, job); err != nil {
				return result, err
			}
			result.Retired++
		}
		output, err := json.Marshal(map[string]any{
			"handoff": "river", "riverJobId": riverJobID, "retiredBy": "go-shared-pipeline",
		})
		if err != nil {
			return result, err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET state='completed',completed_on=now(),output=$2::jsonb WHERE id=$1::uuid`, table), job.ID, output); err != nil {
			return result, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func pgBossRiverJob(job pgBossPendingJob) (river.JobArgs, *river.InsertOpts, bool, error) {
	var payload map[string]json.RawMessage
	if len(job.Data) > 0 {
		if err := json.Unmarshal(job.Data, &payload); err != nil {
			return nil, nil, false, err
		}
	}
	if payload == nil {
		payload = map[string]json.RawMessage{}
	}
	scheduledAt := time.Time{}
	if job.StartAfter.After(time.Now()) {
		scheduledAt = job.StartAfter
	}
	readString := func(key string) (string, error) {
		raw, ok := payload[key]
		if !ok {
			return "", fmt.Errorf("%s is required", key)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || value == "" {
			return "", fmt.Errorf("%s must be a non-empty string", key)
		}
		return value, nil
	}
	readInt := func(key string) (int, error) {
		raw, ok := payload[key]
		if !ok {
			return 0, nil
		}
		var value int
		if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
			return 0, fmt.Errorf("%s must be a non-negative integer", key)
		}
		return value, nil
	}
	remainingAttempts := func(total int) int {
		if job.RetryCount >= total-1 {
			return 1
		}
		return total - job.RetryCount
	}
	switch job.Name {
	case "publication.republish-source":
		sourceID, err := readString("sourceId")
		if err != nil {
			return nil, nil, false, err
		}
		opts := SourceRepublishInsertOpts()
		opts.MaxAttempts = remainingAttempts(3)
		opts.ScheduledAt = scheduledAt
		return SourceRepublishArgs{SourceID: sourceID}, opts, true, nil
	case "content.analyze", "content.extract-body", "events.group", "events.digest":
		// Durable article/story state is the recovery source for these jobs. Their Go scanners
		// reconstruct River work, including failed jobs released from an unknown paid outcome.
		return nil, nil, false, nil
	case "sources.fetch":
		var force bool
		if raw, ok := payload["force"]; ok {
			if err := json.Unmarshal(raw, &force); err != nil {
				return nil, nil, false, errors.New("force must be a boolean")
			}
		}
		sourceID, err := readString("sourceId")
		if err != nil {
			return nil, nil, false, err
		}
		if !force {
			return nil, nil, false, nil // River's due-source scanner reconstructs scheduled work from source state.
		}
		if job.SourceKind == "external" || job.SourceKind == "" {
			return nil, nil, false, nil
		}
		args, opts, err := ManualSourceFetchArgs(sourceID, job.SourceKind)
		if err != nil {
			return nil, nil, false, nil
		}
		sourceArgs := args.(SourceArgs)
		sourceArgs.Force = true
		opts.MaxAttempts = 1
		if !scheduledAt.IsZero() {
			opts.ScheduledAt = scheduledAt
		}
		return sourceArgs, opts, true, nil
	case "sources.fetch-x":
		return nil, nil, false, nil // Go's database-backed source and MP scanners own scheduled work.
	case "sources.mp":
		var reason string
		if raw, ok := payload["reason"]; ok {
			if err := json.Unmarshal(raw, &reason); err != nil {
				return nil, nil, false, errors.New("reason must be a string")
			}
		}
		if reason != "manual" {
			return nil, nil, false, nil
		}
		sourceID, err := readString("sourceId")
		if err != nil {
			return nil, nil, false, err
		}
		return MPCheckArgs{SourceID: sourceID}, &river.InsertOpts{Queue: "mp_accounts", Priority: 2, MaxAttempts: remainingAttempts(4), ScheduledAt: scheduledAt, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		}}}, true, nil
	case "notify.selected":
		articleID, err := readString("articleId")
		if err != nil {
			return nil, nil, false, err
		}
		attempt, err := readInt("attempt")
		if err != nil {
			return nil, nil, false, err
		}
		opts := selectedNotifyInsertOpts(scheduledAt)
		opts.MaxAttempts = 1
		return SelectedNotifyArgs{ArticleID: articleID, Attempt: attempt}, opts, true, nil
	case "media.prepare":
		if raw, ok := payload["articleId"]; ok {
			var articleID string
			if err := json.Unmarshal(raw, &articleID); err != nil || articleID == "" {
				return nil, nil, false, errors.New("articleId must be a non-empty string")
			}
			opts := articleMediaInsertOpts()
			opts.MaxAttempts = remainingAttempts(2)
			opts.ScheduledAt = scheduledAt
			return MediaPrepareArgs{ArticleID: articleID}, opts, true, nil
		}
		rawURL, err := readString("url")
		if err != nil {
			return nil, nil, false, err
		}
		mode, err := readString("mode")
		if err != nil {
			return nil, nil, false, err
		}
		opts := articleMediaInsertOpts()
		opts.MaxAttempts = remainingAttempts(2)
		opts.ScheduledAt = scheduledAt
		return MediaPrepareArgs{URL: rawURL, Mode: mode}, opts, true, nil
	default:
		return nil, nil, false, fmt.Errorf("unsupported queue %q", job.Name)
	}
}

func clearRetiredQueueStamp(ctx context.Context, tx pgx.Tx, job pgBossPendingJob) error {
	if job.Name != "content.analyze" && job.Name != "content.extract-body" {
		return nil
	}
	var payload struct {
		ArticleID string `json:"articleId"`
	}
	if len(job.Data) == 0 || json.Unmarshal(job.Data, &payload) != nil || payload.ArticleID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE articles SET processing_queued_at=NULL
		WHERE id=$1 AND processing_state='new'`, payload.ArticleID)
	return err
}

type PgBossHandoffScanArgs struct{}

func (PgBossHandoffScanArgs) Kind() string { return "myhot_ops_queue_handoff_scan" }

type PgBossHandoffScanWorker struct {
	river.WorkerDefaults[PgBossHandoffScanArgs]
	Pool             *pgxpool.Pool
	Client           *river.Client[pgx.Tx]
	DB               *gorm.DB
	CollectorEnabled bool
	ManualMPEnabled  bool
	DigestEnabled    bool
}

func (w *PgBossHandoffScanWorker) Work(ctx context.Context, _ *river.Job[PgBossHandoffScanArgs]) error {
	return runTrackedTask(ctx, w.DB, "ops.queue.handoff", func() (any, error) {
		return ImportPendingPgBossJobs(ctx, w.Pool, w.Client, w.CollectorEnabled, w.ManualMPEnabled, w.DigestEnabled)
	})
}

func pgBossHandoffPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		return PgBossHandoffScanArgs{}, &river.InsertOpts{Priority: 1, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
	}, &river.PeriodicJobOpts{ID: "myhot-pgboss-handoff", RunOnStart: true})
}
