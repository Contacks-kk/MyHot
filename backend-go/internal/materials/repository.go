package materials

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

type Input struct {
	SourceID     string
	URL          string
	IdentityKey  string
	Title        string
	Author       string
	PublishedAt  *time.Time
	DiscoveredAt time.Time
	Backfill     string
	Excerpt      string
	BodyText     string
	BodyHTML     string
	BodyStatus   string
	Language     string
	Media        json.RawMessage
	XPost        json.RawMessage
	Raw          json.RawMessage
	Via          string
}

type Result struct {
	ArticleID string
	Created   bool
	Revised   bool
	Backfill  bool
}

func (r *Repository) StorePrepared(ctx context.Context, input Input, decision TimelineDecision, hash string) (Result, error) {
	id, err := newArticleID()
	if err != nil {
		return Result{}, err
	}

	result := Result{ArticleID: id, Backfill: decision.Backfill}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		article := model.Article{
			ID: id, SourceID: input.SourceID, IdentityKey: input.IdentityKey, URL: input.URL,
			Title: input.Title, PublishedAt: decision.PublishedAt, PublishedClaim: input.PublishedAt,
			DiscoveredAt: input.DiscoveredAt, TimelineAt: decision.TimelineAt, Backfill: decision.Backfill,
			BackfillReason: optionalString(decision.BackfillReason), ContentHash: &hash,
			Excerpt: optionalString(input.Excerpt), BodyText: optionalString(input.BodyText),
			BodyHTML:   optionalString(input.BodyHTML),
			BodyStatus: input.BodyStatus, Language: optionalString(input.Language), Media: input.Media,
			XPost: input.XPost, Raw: input.Raw, Revision: 1,
		}
		if input.Author != "" {
			article.Author = &input.Author
		}
		insert := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "identity_key"}},
			DoNothing: true,
		}).Create(&article)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 1 {
			result.Created = true
			if err := tx.Create(&model.ArticleRevision{ArticleID: id, Revision: 1, ContentHash: hash, Title: input.Title, BodyText: optionalString(input.BodyText)}).Error; err != nil {
				return err
			}
		} else {
			var existing model.Article
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("identity_key = ?", input.IdentityKey).Take(&existing).Error; err != nil {
				return err
			}
			result.ArticleID = existing.ID
			if err := r.recordExisting(tx, existing, input, decision, hash, &result); err != nil {
				return err
			}
		}

		discovery := model.ArticleDiscovery{ArticleID: result.ArticleID, SourceID: input.SourceID, Via: input.Via, DiscoveredAt: input.DiscoveredAt}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&discovery).Error
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (r *Repository) recordExisting(tx *gorm.DB, old model.Article, input Input, decision TimelineDecision, hash string, result *Result) error {
	result.Backfill = old.Backfill
	var rollout struct {
		ActivatedAt time.Time `gorm:"column:activated_at"`
	}
	rolloutResult := tx.Table("personalization_rollout").Select("activated_at").Where("id = TRUE").Take(&rollout)
	if rolloutResult.Error != nil && !errors.Is(rolloutResult.Error, gorm.ErrRecordNotFound) {
		return rolloutResult.Error
	}
	historical := rolloutResult.RowsAffected > 0 && old.CreatedAt.Before(rollout.ActivatedAt)
	if historical || old.SourceID != input.SourceID {
		return nil
	}

	var oldBody, oldExcerpt string
	if old.BodyText != nil {
		oldBody = *old.BodyText
	}
	if old.Excerpt != nil {
		oldExcerpt = *old.Excerpt
	}
	bodyText := oldBody
	if input.BodyText != "" {
		bodyText = input.BodyText
	}
	if input.Excerpt == "" {
		input.Excerpt = oldExcerpt
	}
	hash = ContentHash(input.Title, bodyText, input.Excerpt)
	decision = TimelineDecision{PublishedAt: old.PublishedAt, TimelineAt: old.TimelineAt, Backfill: old.Backfill}
	timeFilled := false
	if old.PublishedAt == nil && input.PublishedAt != nil {
		explicit := ""
		if old.BackfillReason != nil && *old.BackfillReason != "unknown-publication-time" {
			explicit = *old.BackfillReason
		}
		decision = DecideTimeline(input.PublishedAt, old.DiscoveredAt, explicit)
		timeFilled = decision.PublishedAt != nil
	}
	if old.ContentHash == nil && !timeFilled {
		updates := map[string]any{"content_hash": hash}
		if old.Excerpt == nil && input.Excerpt != "" {
			updates["excerpt"] = input.Excerpt
		}
		if err := tx.Model(&model.Article{}).Where("id = ?", old.ID).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ArticleRevision{
			ArticleID: old.ID, Revision: old.Revision, ContentHash: hash, Title: input.Title, BodyText: optionalString(bodyText),
		}).Error
	}
	if input.BodyText == "" && !timeFilled {
		return nil
	}
	if old.ContentHash != nil && *old.ContentHash == hash && !timeFilled {
		return nil
	}
	if input.BodyText != "" && old.BodyText != nil && len(input.BodyText) < len(*old.BodyText) && !timeFilled {
		return nil
	}
	if !timeFilled && SameBarringLoss(old.Title, input.Title) && SameBarringLoss(oldBody, bodyText) && SameBarringLoss(oldExcerpt, input.Excerpt) {
		return nil
	}

	var seen int64
	if err := tx.Model(&model.ArticleRevision{}).Where("article_id = ? AND content_hash = ?", old.ID, hash).Limit(1).Count(&seen).Error; err != nil {
		return err
	}
	if seen > 0 && !timeFilled {
		return nil
	}

	updates := map[string]any{
		"title": input.Title, "content_hash": hash, "revision": old.Revision + 1,
		"processing_state": "new", "processing_attempts": 0, "processing_retry_at": nil,
		"processing_error": nil, "processing_queued_at": nil,
		"grouping_status": "pending", "grouped_at": nil, "grouping_receipt_id": nil,
		"grouping_error": nil, "selection_adds_value": nil, "selection_value_reason": nil,
		"updated_at": gorm.Expr("now()"),
	}
	if input.BodyText != "" {
		updates["body_text"] = input.BodyText
		updates["body_status"] = "ok"
	}
	if input.BodyHTML != "" {
		updates["body_html"] = input.BodyHTML
	}
	if input.Author != "" {
		updates["author"] = input.Author
	}
	if input.Excerpt != "" {
		updates["excerpt"] = input.Excerpt
	}
	if input.Language != "" {
		updates["language"] = input.Language
	}
	if len(input.Media) > 0 {
		updates["media"] = input.Media
	}
	if len(input.XPost) > 0 {
		updates["x_post"] = input.XPost
	}
	if timeFilled {
		updates["published_at"] = decision.PublishedAt
		updates["published_at_claim"] = input.PublishedAt
		updates["timeline_at"] = decision.TimelineAt
		updates["backfill"] = decision.Backfill
		updates["backfill_reason"] = optionalString(decision.BackfillReason)
	}
	if err := tx.Model(&model.Article{}).Where("id = ?", old.ID).Updates(updates).Error; err != nil {
		return err
	}
	if err := tx.Create(&model.ArticleRevision{
		ArticleID: old.ID, Revision: old.Revision + 1, ContentHash: hash, Title: input.Title, BodyText: optionalString(bodyText),
	}).Error; err != nil {
		return err
	}
	result.Revised = true
	result.Backfill = decision.Backfill
	return nil
}

func newArticleID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "m" + hex.EncodeToString(bytes), nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
