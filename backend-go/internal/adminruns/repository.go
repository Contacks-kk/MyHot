package adminruns

import (
	"context"
	"encoding/json"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) Load(ctx context.Context) (Data, error) {
	data := Data{}
	queries := []struct {
		dest any
		sql  string
	}{
		{&data.Heartbeats, `SELECT key, value, updated_at FROM settings WHERE key LIKE 'heartbeat.%' ORDER BY key`},
		{&data.Jobs, `WITH latest AS (
			SELECT DISTINCT ON (job) job, started_at, finished_at, status, left(error, 400) AS error
			FROM job_runs ORDER BY job, started_at DESC, id DESC
		), counts AS (
			SELECT job, count(*) FILTER (WHERE status = 'failed')::int AS failed_24h, count(*)::int AS runs_24h
			FROM job_runs WHERE started_at > now() - interval '24 hours' GROUP BY job
		)
		SELECT latest.*, coalesce(counts.failed_24h, 0) AS failed_24h, coalesce(counts.runs_24h, 0) AS runs_24h
		FROM latest LEFT JOIN counts USING (job) ORDER BY job`},
		{&data.Timeline, `SELECT id, job, started_at, finished_at, status, left(error, 300) AS error
			FROM job_runs ORDER BY started_at DESC, id DESC LIMIT 80`},
		{&data.Lagging, `SELECT id, name, kind, health, fail_count, last_ok_at, last_fetch_at, next_fetch_at, interval_minutes, left(last_error, 200) AS last_error
			FROM sources WHERE enabled AND kind NOT IN ('mp_account', 'external')
			AND (health = 'failing' OR next_fetch_at < now() - interval '30 minutes'
			OR last_ok_at < now() - make_interval(mins => greatest(interval_minutes * 6, 360)))
			ORDER BY health = 'failing' DESC, next_fetch_at, id LIMIT 60`},
		{&data.Receipts, `SELECT status, count(*)::int AS n FROM receipts WHERE created_at > now() - interval '7 days' GROUP BY 1 ORDER BY 1`},
		{&data.ReceiptIssues, `SELECT id, service, model, purpose, subject, status, attempts, left(error, 240) AS error, created_at, updated_at FROM receipts
			WHERE status = 'unknown' OR (status = 'failed' AND updated_at > now() - interval '3 days')
			OR (status = 'pending' AND updated_at < now() - interval '15 minutes')
			ORDER BY status = 'unknown' DESC, updated_at DESC, id DESC LIMIT 40`},
		{&data.Deliveries, `SELECT id, target_key, subject_kind, subject_id, status, attempts, left(response, 240) AS response, created_at, updated_at FROM deliveries
			WHERE status IN ('unknown', 'failed') OR (status = 'sending' AND updated_at < now() - interval '15 minutes')
			ORDER BY status = 'unknown' DESC, updated_at DESC, id DESC LIMIT 40`},
		{&data.Errors, `SELECT regexp_replace(left(coalesce(processing_error, '(no message)'), 120), '[0-9a-f]{8,}|[0-9]{4,}', '…', 'g') AS error,
			count(*)::int AS n, max(discovered_at) AS last, (array_agg(id ORDER BY discovered_at DESC))[1] AS example
			FROM articles WHERE processing_state = 'failed' AND discovered_at > now() - interval '30 days'
			GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 20`},
		{&data.Ingest, `SELECT client, kind, status, left(error, 200) AS error, summary, created_at FROM ingest_events ORDER BY created_at DESC, id DESC LIMIT 20`},
		{&data.Retrying, `SELECT count(*)::int AS count, min(processing_retry_at) AS next FROM articles WHERE processing_state = 'new' AND processing_attempts > 0`},
	}
	for _, query := range queries {
		if err := r.db.WithContext(ctx).Raw(query.sql).Scan(query.dest).Error; err != nil {
			return Data{}, err
		}
	}
	var pgbossExists, riverExists bool
	if err := r.db.WithContext(ctx).Raw("SELECT to_regclass('pgboss.job') IS NOT NULL").Scan(&pgbossExists).Error; err != nil {
		return Data{}, err
	}
	if err := r.db.WithContext(ctx).Raw("SELECT to_regclass('river_job') IS NOT NULL").Scan(&riverExists).Error; err != nil {
		return Data{}, err
	}
	if pgbossExists {
		if err := r.db.WithContext(ctx).Raw(`SELECT name, state, count(*)::int AS n, min(created_on) AS oldest FROM pgboss.job
			WHERE state IN ('created', 'retry', 'active') GROUP BY 1, 2 ORDER BY 1, 2`).Scan(&data.Queues).Error; err != nil {
			return Data{}, err
		}
		var failed []FailedJob
		if err := r.db.WithContext(ctx).Raw(`SELECT name, count(*)::int AS failed, max(completed_on) AS last,
			left((array_agg(output::text ORDER BY completed_on DESC))[1], 300) AS last_output
			FROM pgboss.job WHERE state = 'failed' AND completed_on > now() - interval '24 hours' GROUP BY 1 ORDER BY 2 DESC, 1`).Scan(&failed).Error; err != nil {
			return Data{}, err
		}
		data.FailedJobs = append(data.FailedJobs, failed...)
	}
	if riverExists {
		var queued []Queue
		if err := r.db.WithContext(ctx).Raw(`SELECT 'river:' || queue AS name,
			CASE state WHEN 'running' THEN 'active' WHEN 'retryable' THEN 'retry' ELSE 'created' END AS state,
			count(*)::int AS n, min(created_at) AS oldest FROM river_job
			WHERE state IN ('available', 'scheduled', 'retryable', 'running')
			GROUP BY queue, CASE state WHEN 'running' THEN 'active' WHEN 'retryable' THEN 'retry' ELSE 'created' END
			ORDER BY 1, 2`).Scan(&queued).Error; err != nil {
			return Data{}, err
		}
		data.Queues = append(data.Queues, queued...)
		var failed []FailedJob
		if err := r.db.WithContext(ctx).Raw(`SELECT 'river:' || kind AS name, count(*)::int AS failed, max(finalized_at) AS last,
			left((array_agg(coalesce(errors[cardinality(errors)]->>'error', '') ORDER BY finalized_at DESC))[1], 300) AS last_output
			FROM river_job WHERE state = 'discarded' AND finalized_at > now() - interval '24 hours'
			GROUP BY kind ORDER BY 2 DESC, 1`).Scan(&failed).Error; err != nil {
			return Data{}, err
		}
		data.FailedJobs = append(data.FailedJobs, failed...)
	}
	if err := r.db.WithContext(ctx).Raw(groupingOverviewSQL(pgbossExists, riverExists)).Scan(&data.Grouping).Error; err != nil {
		return Data{}, err
	}
	return data, nil
}

func groupingOverviewSQL(pgbossExists, riverExists bool) string {
	queuedAt := `, (SELECT max(h.created_at) FROM audit_log h WHERE h.subject = 'content:' || a.id AND h.action = 'content.rerun.group')`
	var running string
	if pgbossExists {
		queuedAt = `, (SELECT max(j.created_on) FROM pgboss.job j WHERE j.name = 'events.group' AND j.data->>'articleId' = a.id
			AND j.singleton_key LIKE 'manual:group:%'), (SELECT max(h.created_at) FROM audit_log h WHERE h.subject = 'content:' || a.id AND h.action = 'content.rerun.group')`
		running = `EXISTS (SELECT 1 FROM pgboss.job j WHERE j.name = 'events.group'
			AND j.state IN ('created', 'retry', 'active') AND j.data->>'articleId' = a.id)`
	}
	if riverExists {
		if running != "" {
			running += " OR "
		}
		running += `EXISTS (SELECT 1 FROM river_job j WHERE j.kind LIKE 'myhot_%group%'
			AND j.state IN ('available', 'scheduled', 'retryable', 'running') AND j.args->>'articleId' = a.id)`
	}
	if running == "" {
		running = "false"
	}
	return `SELECT p.article_id AS article_id, p.title,
		greatest(coalesce(p.selected_ready_at, p.discovered_at), revision.created_at` + queuedAt + `) AS since,
		a.grouping_status = 'failed' AS failed, a.grouping_receipt_id AS receipt_id, a.grouping_error AS error,
		r.status AS receipt_status, (` + running + `) AS running,
		EXISTS (SELECT 1 FROM receipt_attempts attempt WHERE attempt.receipt_id = r.id AND attempt.error LIKE '自动放行：结果未知超过 30 分钟，未核对是否计费%') AS released
		FROM publications p JOIN articles a ON a.id = p.article_id LEFT JOIN receipts r ON r.id = a.grouping_receipt_id
		LEFT JOIN article_revisions revision ON revision.article_id = a.id AND revision.revision = a.revision
		WHERE p.selection_candidate AND p.eligible AND p.visibility = 'public' AND a.grouping_status <> 'complete'
		ORDER BY since, p.article_id`
}

func (r *GormRepository) RequeueFailed(ctx context.Context, group *string, reason, actor, subject string) (RequeueResult, error) {
	var result RequeueResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.ArticleProcessing{}).
			Where("processing_state = ? AND discovered_at > now() - interval '30 days'", "failed").
			Where("(?::text IS NULL OR regexp_replace(left(coalesce(processing_error, '(no message)'), 120), '[0-9a-f]{8,}|[0-9]{4,}', '…', 'g') = ?)", group, group).
			Updates(map[string]any{
				"processing_state":     "new",
				"processing_attempts":  0,
				"processing_retry_at":  nil,
				"processing_error":     nil,
				"processing_queued_at": nil,
			})
		if updated.Error != nil {
			return updated.Error
		}
		result.Requeued = updated.RowsAffected
		after, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.AdminAuditLog{
			Actor: actor, Action: "processing.requeue", Subject: &subject, Reason: &reason,
			Before: json.RawMessage("null"), After: after,
		}).Error
	})
	return result, err
}
