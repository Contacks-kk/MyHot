package contentpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"aihot/backend-go/internal/articlebody"
	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct {
	db                  *gorm.DB
	bodyPipelineEnabled bool
}

func NewRepository(db *gorm.DB, bodyPipelineEnabled ...bool) *GormRepository {
	repository := &GormRepository{db: db}
	if len(bodyPipelineEnabled) > 0 {
		repository.bodyPipelineEnabled = bodyPipelineEnabled[0]
	}
	return repository
}

type analysisCandidate struct {
	ArticleID         string          `gorm:"column:article_id"`
	AttemptTag        string          `gorm:"column:attempt_tag"`
	ProcessingState   string          `gorm:"column:processing_state"`
	URL               string          `gorm:"column:url"`
	SourceKind        string          `gorm:"column:source_kind"`
	ParticipationMode string          `gorm:"column:participation_mode"`
	BodyStatus        string          `gorm:"column:body_status"`
	BodyTextPresent   bool            `gorm:"column:body_text_present"`
	Revision          int             `gorm:"column:revision"`
	DiscoveredAt      time.Time       `gorm:"column:discovered_at"`
	PublishedAt       *time.Time      `gorm:"column:published_at"`
	Backfill          bool            `gorm:"column:backfill"`
	BackfillReason    *string         `gorm:"column:backfill_reason"`
	SourceConfig      json.RawMessage `gorm:"column:source_config;type:jsonb"`
	XPostPresent      bool            `gorm:"column:x_post_present"`
	XPost             json.RawMessage `gorm:"column:x_post;type:jsonb"`
	HasPublication    bool            `gorm:"column:has_publication"`
}

func (r *GormRepository) PendingAnalysis(ctx context.Context, limit int) ([]AnalysisTask, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var candidates []analysisCandidate
	err := r.db.WithContext(ctx).Raw(`SELECT a.id AS article_id, coalesce(a.processing_attempt_tag, '') AS attempt_tag,
		a.url, s.kind AS source_kind, s.participation_mode, a.body_status,
		coalesce(a.body_text, '') <> '' AS body_text_present, a.revision, a.discovered_at, a.published_at,
		a.backfill, a.backfill_reason, s.config AS source_config, (a.x_post IS NOT NULL) AS x_post_present, a.x_post,
		EXISTS(SELECT 1 FROM publications p WHERE p.article_id=a.id) AS has_publication
		FROM articles a JOIN sources s ON s.id=a.source_id
		WHERE a.processing_state='new' AND a.created_at < now() - interval '3 minutes'
		AND (processing_retry_at IS NULL OR processing_retry_at <= now())
		ORDER BY a.discovered_at DESC LIMIT ?`, min(limit*10, 5000)).Scan(&candidates).Error
	if err != nil {
		return nil, err
	}
	rows := make([]AnalysisTask, 0, min(limit, len(candidates)))
	for _, candidate := range candidates {
		bodyCandidate := articlebody.Candidate{
			ID: candidate.ArticleID, URL: candidate.URL, BodyStatus: candidate.BodyStatus,
			Revision: candidate.Revision, SourceKind: candidate.SourceKind,
			ParticipationMode: candidate.ParticipationMode, SourceConfig: candidate.SourceConfig,
			XPostPresent: candidate.XPostPresent, XPost: candidate.XPost, HasPublication: candidate.HasPublication,
			Bare: !candidate.BodyTextPresent && !candidate.XPostPresent, Backfill: candidate.Backfill,
			PublishedAt: candidate.PublishedAt, DiscoveredAt: candidate.DiscoveredAt,
			AllowHotSignal: r.bodyPipelineEnabled,
		}
		if candidate.BodyTextPresent {
			bodyText := "present"
			bodyCandidate.BodyText = &bodyText
		}
		if needsBody(bodyCandidate, r.bodyPipelineEnabled) {
			continue
		}
		historical := candidate.Backfill && (candidate.PublishedAt == nil || candidate.DiscoveredAt.Sub(*candidate.PublishedAt) > 48*time.Hour)
		rows = append(rows, AnalysisTask{
			ArticleID: candidate.ArticleID, AttemptTag: candidate.AttemptTag,
			Priority: taskPriority(historical, candidate.ParticipationMode),
		})
		if len(rows) == limit {
			break
		}
	}
	if rows == nil {
		rows = []AnalysisTask{}
	}
	return rows, nil
}

func (r *GormRepository) PendingGrouping(ctx context.Context, limit int) ([]GroupingTask, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var rows []GroupingTask
	err := r.db.WithContext(ctx).Raw(`SELECT a.id AS article_id, (s.participation_mode <> 'editorial') AS signal_only,
		CASE WHEN a.backfill AND (a.published_at IS NULL OR a.discovered_at-a.published_at > interval '48 hours') THEN 3
			WHEN s.participation_mode='hot_signal' THEN 2 ELSE 1 END AS priority
		FROM articles a JOIN sources s ON s.id=a.source_id
		WHERE a.grouping_status='pending' AND a.created_at < now() - interval '3 minutes'
		AND ((a.processing_state='analyzed' AND s.participation_mode='editorial' AND EXISTS (
			SELECT 1 FROM analyses an WHERE an.article_id=a.id AND an.input_revision=a.revision AND an.relevance='pass'
		)) OR (a.processing_state='skipped' AND s.participation_mode='hot_signal'
			AND NOT (a.backfill AND (a.published_at IS NULL OR a.discovered_at-a.published_at > interval '48 hours')))
		OR EXISTS (SELECT 1 FROM fact_articles fa WHERE fa.article_id=a.id AND fa.manual)
		OR EXISTS (SELECT 1 FROM grouping_overrides go WHERE go.article_id=a.id))
		ORDER BY priority ASC, a.discovered_at DESC LIMIT ?`, limit).Scan(&rows).Error
	if rows == nil {
		rows = []GroupingTask{}
	}
	return rows, err
}

func (r *GormRepository) Route(ctx context.Context, articleID string) (*ArticleRoute, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	var rows []analysisCandidate
	err := r.db.WithContext(ctx).Raw(`SELECT a.id AS article_id, a.revision, a.processing_state, s.participation_mode,
		a.backfill, a.published_at, a.discovered_at, a.backfill_reason, a.url, s.kind AS source_kind, a.body_status,
		coalesce(a.body_text, '') <> '' AS body_text_present, s.config AS source_config,
		(a.x_post IS NOT NULL) AS x_post_present, a.x_post,
		EXISTS(SELECT 1 FROM publications p WHERE p.article_id=a.id) AS has_publication
		FROM articles a JOIN sources s ON s.id=a.source_id WHERE a.id=?`, articleID).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	row := rows[0]
	bodyCandidate := articlebody.Candidate{
		ID: row.ArticleID, URL: row.URL, BodyStatus: row.BodyStatus, Revision: row.Revision,
		SourceKind: row.SourceKind, ParticipationMode: row.ParticipationMode, SourceConfig: row.SourceConfig,
		XPostPresent: row.XPostPresent, XPost: row.XPost, HasPublication: row.HasPublication,
		Bare: !row.BodyTextPresent && !row.XPostPresent, Backfill: row.Backfill,
		PublishedAt: row.PublishedAt, DiscoveredAt: row.DiscoveredAt,
		AllowHotSignal: r.bodyPipelineEnabled,
	}
	if row.BodyTextPresent {
		bodyText := "present"
		bodyCandidate.BodyText = &bodyText
	}
	return &ArticleRoute{
		ID: row.ArticleID, Revision: row.Revision, ProcessingState: row.ProcessingState,
		ParticipationMode: row.ParticipationMode, NeedsBody: needsBody(bodyCandidate, r.bodyPipelineEnabled),
		Backfill: row.Backfill, PublishedAt: row.PublishedAt, DiscoveredAt: row.DiscoveredAt,
	}, nil
}

func (r *GormRepository) CurrentAnalysis(ctx context.Context, articleID string, revision int) (string, bool, error) {
	if err := r.validate(); err != nil {
		return "", false, err
	}
	var rows []struct {
		Relevance string `gorm:"column:relevance"`
	}
	err := r.db.WithContext(ctx).Raw(`SELECT relevance FROM analyses WHERE article_id=? AND input_revision=?
		ORDER BY id DESC LIMIT 1`, articleID, revision).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	return rows[0].Relevance, true, nil
}

func (r *GormRepository) MarkSkipped(ctx context.Context, articleID string, revision int) error {
	if err := r.validate(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&model.ArticleProcessing{}).Where("id=? AND revision=? AND processing_state='new'", articleID, revision).Updates(map[string]any{
		"processing_state": "skipped", "processing_attempts": 0, "processing_retry_at": nil,
		"processing_queued_at": nil, "processing_error": nil, "updated_at": gorm.Expr("now()"),
	}).Error
}

func (r *GormRepository) CompleteAnalysis(ctx context.Context, articleID string, revision int) error {
	if err := r.validate(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&model.ArticleProcessing{}).Where("id=? AND revision=?", articleID, revision).Updates(map[string]any{
		"processing_attempts": 0, "processing_retry_at": nil, "processing_queued_at": nil,
		"processing_error": nil, "updated_at": gorm.Expr("now()"),
	}).Error
}

func (r *GormRepository) DeferAnalysis(ctx context.Context, articleID string, revision int, message string, delay time.Duration) (time.Time, error) {
	if err := r.validate(); err != nil {
		return time.Time{}, err
	}
	var rows []struct {
		RetryAt time.Time `gorm:"column:retry_at"`
	}
	seconds := max(int64(1), int64(delay.Seconds()))
	err := r.db.WithContext(ctx).Raw(`UPDATE articles SET processing_state='new', processing_error=?,
		processing_retry_at=now()+(? * interval '1 second'), processing_queued_at=NULL, updated_at=now()
		WHERE id=? AND revision=? RETURNING processing_retry_at AS retry_at`, truncate(message, 500), seconds, articleID, revision).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return time.Time{}, err
	}
	return rows[0].RetryAt, nil
}

func (r *GormRepository) RecordAnalysisFailure(ctx context.Context, articleID string, revision int, message string, outputFailure bool) (FailureResult, error) {
	if err := r.validate(); err != nil {
		return FailureResult{}, err
	}
	var result FailureResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			Attempts int `gorm:"column:processing_attempts"`
		}
		if err := tx.Raw(`SELECT processing_attempts FROM articles WHERE id=? AND revision=? FOR UPDATE`, articleID, revision).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			result.State = "stale"
			return nil
		}
		attempts := rows[0].Attempts + 1
		delay, canRetry := analysisRetryDelay(attempts)
		if !canRetry || outputFailure && attempts >= 3 {
			result.State = "failed"
			return tx.Model(&model.ArticleProcessing{}).Where("id=? AND revision=?", articleID, revision).Updates(map[string]any{
				"processing_state": "failed", "processing_attempts": attempts, "processing_retry_at": nil,
				"processing_queued_at": nil, "processing_error": truncate(message, 500), "updated_at": gorm.Expr("now()"),
			}).Error
		}
		var retryAt []struct {
			RetryAt time.Time `gorm:"column:retry_at"`
		}
		if err := tx.Raw(`UPDATE articles SET processing_state='new', processing_attempts=?, processing_error=?,
			processing_retry_at=now()+(? * interval '1 minute'), processing_queued_at=NULL, updated_at=now()
			WHERE id=? AND revision=? RETURNING processing_retry_at AS retry_at`, attempts, truncate(message, 500), int(delay.Minutes()), articleID, revision).Scan(&retryAt).Error; err != nil {
			return err
		}
		if len(retryAt) > 0 {
			result.RetryAt, result.State = retryAt[0].RetryAt, "retrying"
		} else {
			result.State = "stale"
		}
		return nil
	})
	return result, err
}

func (r *GormRepository) FailAnalysis(ctx context.Context, articleID string, revision int, message string, increment bool) error {
	if err := r.validate(); err != nil {
		return err
	}
	updates := map[string]any{
		"processing_state": "failed", "processing_retry_at": nil, "processing_queued_at": nil,
		"processing_error": truncate(message, 500), "updated_at": gorm.Expr("now()"),
	}
	if increment {
		updates["processing_attempts"] = gorm.Expr("processing_attempts + 1")
	}
	return r.db.WithContext(ctx).Model(&model.ArticleProcessing{}).Where("id=? AND revision=?", articleID, revision).Updates(updates).Error
}

func (r *GormRepository) validate() error {
	if r == nil || r.db == nil {
		return errors.New("content pipeline repository is unavailable")
	}
	return nil
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
