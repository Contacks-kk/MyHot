package articlebody

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNotFound = errors.New("article was not found")

type Candidate struct {
	ID                         string          `gorm:"column:id"`
	URL                        string          `gorm:"column:url"`
	Title                      string          `gorm:"column:title"`
	Excerpt                    *string         `gorm:"column:excerpt"`
	ContentHash                *string         `gorm:"column:content_hash"`
	BodyText                   *string         `gorm:"column:body_text"`
	BodyStatus                 string          `gorm:"column:body_status"`
	Revision                   int             `gorm:"column:revision"`
	DiscoveredAt               time.Time       `gorm:"column:discovered_at"`
	PublishedAt                *time.Time      `gorm:"column:published_at"`
	BackfillReason             *string         `gorm:"column:backfill_reason"`
	Backfill                   bool            `gorm:"column:backfill"`
	Media                      json.RawMessage `gorm:"column:media;type:jsonb"`
	SourceKind                 string          `gorm:"column:source_kind"`
	ParticipationMode          string          `gorm:"column:participation_mode"`
	SourceConfig               json.RawMessage `gorm:"column:source_config;type:jsonb"`
	XPostPresent               bool            `gorm:"column:x_post_present"`
	XPost                      json.RawMessage `gorm:"column:x_post;type:jsonb"`
	XArticle                   json.RawMessage `gorm:"column:x_article;type:jsonb"`
	PublishedTimeAuthoritative bool            `gorm:"column:published_time_authoritative"`
	Bare                       bool            `gorm:"column:bare"`
	HasPublication             bool            `gorm:"column:has_publication"`
	AllowHotSignal             bool            `gorm:"-"`
}

type Repository struct {
	db               *gorm.DB
	pool             *pgxpool.Pool
	publisher        *publication.Repository
	includeX         bool
	includeHotSignal bool
}

func NewRepository(db *gorm.DB, includeX ...bool) *Repository {
	repository := &Repository{db: db}
	if len(includeX) > 0 {
		repository.includeX = includeX[0]
	}
	if len(includeX) > 1 {
		repository.includeHotSignal = includeX[1]
	}
	return repository
}

// NewTransactionalRepository is used when the full shared pipeline is enabled. Body revision,
// withdrawal/reprojection, and River side effects then share one pgx transaction.
func NewTransactionalRepository(db *gorm.DB, pool *pgxpool.Pool, publisher *publication.Repository, includeX ...bool) *Repository {
	repository := NewRepository(db, includeX...)
	repository.pool = pool
	repository.publisher = publisher
	return repository
}

const candidateColumns = `a.id, a.url, a.title, a.excerpt, a.content_hash, a.body_text, a.body_status, a.revision,
	a.discovered_at, a.published_at, a.backfill_reason, a.backfill, a.media, s.kind AS source_kind,
	s.participation_mode, s.config AS source_config, (a.x_post IS NOT NULL) AS x_post_present,
	coalesce(s.config->'detail'->>'publishedAtAuthoritative', 'false') = 'true' AS published_time_authoritative,
	(coalesce(a.body_text, '') = '' AND a.x_post IS NULL) AS bare,
	EXISTS (SELECT 1 FROM publications p WHERE p.article_id = a.id) AS has_publication, a.x_post, a.x_article`

func (r *Repository) Pending(ctx context.Context, limit int) ([]Candidate, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var candidates []Candidate
	bodyKinds := `(s.config->>'fetchPublicContent' = 'true' OR (jsonb_exists(s.config, 'detail') AND s.config->'detail' <> 'null') OR s.kind = 'web_list'
		OR (coalesce(a.body_text, '') = '' AND a.x_post IS NULL))`
	if r.includeX {
		bodyKinds = strings.TrimSuffix(bodyKinds, ")") + ` OR (s.kind = 'x_search' AND jsonb_exists(a.x_post, 'tweetId')))`
	}
	query := `SELECT ` + candidateColumns + `
		FROM articles a JOIN sources s ON s.id = a.source_id
		WHERE a.body_status = 'pending' AND a.processing_state = 'new'
		  AND a.created_at < now() - interval '3 minutes'
		  AND (a.processing_retry_at IS NULL OR a.processing_retry_at <= now())
		  AND (a.processing_queued_at IS NULL OR a.processing_queued_at < now() - interval '30 minutes')
		  AND ((s.participation_mode = 'editorial' AND ` + bodyKinds + `)`
	if r.includeHotSignal {
		query += ` OR (s.participation_mode = 'hot_signal' AND s.kind = 'x_search'
			AND jsonb_exists(a.x_post, 'tweetId')
			AND NOT (a.backfill AND (a.published_at IS NULL OR a.discovered_at-a.published_at > interval '48 hours')))`
	}
	query += `)`
	if !r.includeHotSignal {
		query += ` AND NOT EXISTS (SELECT 1 FROM publications p WHERE p.article_id = a.id)`
	}
	query += ` ORDER BY a.discovered_at DESC LIMIT ?`
	err := r.db.WithContext(ctx).Raw(query, limit).Scan(&candidates).Error
	for index := range candidates {
		candidates[index].AllowHotSignal = r.includeHotSignal
	}
	if candidates == nil {
		candidates = []Candidate{}
	}
	return candidates, err
}

func (r *Repository) Find(ctx context.Context, id string) (Candidate, error) {
	var candidate Candidate
	err := r.db.WithContext(ctx).Raw(`SELECT `+candidateColumns+`
		FROM articles a JOIN sources s ON s.id = a.source_id WHERE a.id = ?`, id).Scan(&candidate).Error
	if err != nil {
		return Candidate{}, err
	}
	if candidate.ID == "" {
		return Candidate{}, ErrNotFound
	}
	candidate.AllowHotSignal = r.includeHotSignal
	return candidate, nil
}

// MarkUnconfirmed records that the page had no usable body. No article revision is created.
func (r *Repository) MarkUnconfirmed(ctx context.Context, id string, revision int) (bool, error) {
	result := r.db.WithContext(ctx).Model(&model.Article{}).Where("id = ? AND revision = ? AND body_status = 'pending'", id, revision).Updates(map[string]any{
		"body_status": "unconfirmed", "processing_attempts": 0, "processing_retry_at": nil,
		"processing_queued_at": nil, "updated_at": gorm.Expr("now()"),
	})
	return result.RowsAffected > 0, result.Error
}

// RecordFailure persists the legacy extraction backoff. The third failure is judged on the feed excerpt.
func (r *Repository) RecordFailure(ctx context.Context, id string, revision int, message string) (attempts int, terminal bool, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var article model.Article
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND revision = ? AND body_status = 'pending'", id, revision).Take(&article)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if result.Error != nil {
			return result.Error
		}
		var current struct {
			Attempts int `gorm:"column:processing_attempts"`
		}
		if err := tx.Raw("SELECT processing_attempts FROM articles WHERE id = ?", id).Scan(&current).Error; err != nil {
			return err
		}
		attempts = current.Attempts + 1
		updates := map[string]any{
			"processing_attempts": attempts, "processing_error": truncate(message, 500),
			"processing_queued_at": nil, "processing_retry_at": gorm.Expr("now() + interval '10 minutes'"),
			"updated_at": gorm.Expr("now()"),
		}
		if attempts >= 3 {
			terminal = true
			updates["body_status"] = "unconfirmed"
			updates["processing_attempts"] = 0
			updates["processing_retry_at"] = nil
		}
		return tx.Model(&model.Article{}).Where("id = ? AND revision = ?", id, revision).Updates(updates).Error
	})
	return attempts, terminal, err
}

// SaveBody applies a fetched body under a row lock and keeps article revision and reset state atomic.
func (r *Repository) SaveBody(ctx context.Context, candidate Candidate, body *Extracted) (bool, error) {
	if r.pool != nil && r.publisher != nil {
		return r.saveBodyAndProjection(ctx, candidate, body)
	}
	if body == nil {
		return r.MarkUnconfirmed(ctx, candidate.ID, candidate.Revision)
	}
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Article
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND revision = ? AND body_status = 'pending'", candidate.ID, candidate.Revision).Take(&current)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if result.Error != nil {
			return result.Error
		}
		var published int64
		if err := tx.Table("publications").Where("article_id = ?", candidate.ID).Limit(1).Count(&published).Error; err != nil {
			return err
		}
		if published > 0 && !candidate.AllowHotSignal {
			return nil
		}
		oldExcerpt := stringValue(current.Excerpt)
		title := current.Title
		if body.Title != "" {
			title = body.Title
		}
		hash := materials.ContentHash(title, body.Text, oldExcerpt)
		var timeline *materials.TimelineDecision
		if current.PublishedAt == nil && body.PublishedAt != nil && !candidate.PublishedTimeAuthoritative {
			explicit := ""
			if current.BackfillReason != nil && *current.BackfillReason != "unknown-publication-time" {
				explicit = *current.BackfillReason
			}
			decision := materials.DecideTimeline(body.PublishedAt, current.DiscoveredAt, explicit)
			if decision.PublishedAt != nil {
				timeline = &decision
			}
		}
		if timeline == nil && current.ContentHash != nil && *current.ContentHash == hash {
			updates := map[string]any{
				"body_status": "ok", "processing_attempts": 0, "processing_retry_at": nil,
				"processing_queued_at": nil, "updated_at": gorm.Expr("now()"),
			}
			if len(body.XArticle) > 0 {
				updates["x_article"] = gorm.Expr("?::jsonb", string(body.XArticle))
			}
			return tx.Model(&model.Article{}).Where("id = ?", current.ID).Updates(updates).Error
		}
		if len(body.XArticle) == 0 && timeline == nil && current.BodyText != nil && len(body.Text) < len(*current.BodyText) {
			updates := map[string]any{
				"body_status": "ok", "processing_attempts": 0, "processing_retry_at": nil,
				"processing_queued_at": nil, "updated_at": gorm.Expr("now()"),
			}
			if len(body.XArticle) > 0 {
				updates["x_article"] = gorm.Expr("?::jsonb", string(body.XArticle))
			}
			return tx.Model(&model.Article{}).Where("id = ?", current.ID).Updates(updates).Error
		}
		if len(body.XArticle) == 0 && timeline == nil && current.BodyText != nil && materials.SameBarringLoss(*current.BodyText, body.Text) {
			updates := map[string]any{
				"body_status": "ok", "processing_attempts": 0, "processing_retry_at": nil,
				"processing_queued_at": nil, "updated_at": gorm.Expr("now()"),
			}
			if len(body.XArticle) > 0 {
				updates["x_article"] = gorm.Expr("?::jsonb", string(body.XArticle))
			}
			return tx.Model(&model.Article{}).Where("id = ?", current.ID).Updates(updates).Error
		}

		imageJSON, err := json.Marshal(body.Images)
		if err != nil {
			return err
		}
		updates := map[string]any{
			"body_text": body.Text, "body_status": "ok",
			"content_hash": hash, "revision": current.Revision + 1,
			"processing_state": "new", "processing_attempts": 0, "processing_retry_at": nil,
			"processing_error": nil, "processing_queued_at": nil,
			"grouping_status": "pending", "grouped_at": nil, "grouping_receipt_id": nil,
			"grouping_error": nil, "selection_adds_value": nil, "selection_value_reason": nil,
			"media":      gorm.Expr("CASE WHEN coalesce(jsonb_array_length(media), 0) = 0 THEN ?::jsonb ELSE media END", string(imageJSON)),
			"updated_at": gorm.Expr("now()"),
		}
		if body.HTML != "" {
			updates["body_html"] = body.HTML
		}
		if body.Title != "" {
			updates["title"] = title
		}
		if len(body.XArticle) > 0 {
			updates["x_article"] = gorm.Expr("?::jsonb", string(body.XArticle))
		}
		if timeline != nil {
			updates["published_at"] = timeline.PublishedAt
			updates["published_at_claim"] = body.PublishedAt
			updates["timeline_at"] = timeline.TimelineAt
			updates["backfill"] = timeline.Backfill
			updates["backfill_reason"] = nullableString(timeline.BackfillReason)
		}
		if err := tx.Model(&model.Article{}).Where("id = ? AND revision = ?", current.ID, current.Revision).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.ArticleRevision{ArticleID: current.ID, Revision: current.Revision + 1, ContentHash: hash, Title: title, BodyText: &body.Text}).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
