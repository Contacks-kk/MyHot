package storydigest

import (
	"context"
	"errors"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) Story(ctx context.Context, id int64) (*Story, error) {
	var row model.Story
	err := r.db.WithContext(ctx).Where("id = ? AND merged_into IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Story{ID: row.ID, Title: row.Title, Digest: row.Digest, Version: row.Version, Origin: row.Origin}, nil
}

func (r *GormRepository) Reports(ctx context.Context, storyID int64, now time.Time) ([]DigestReport, error) {
	return digestReports(r.db.WithContext(ctx), storyID, now)
}

func (r *GormRepository) LastDigest(ctx context.Context, storyID int64) (*SavedDigest, error) {
	var row struct {
		ArticleIDs model.TextArray `gorm:"column:article_ids;type:text[]"`
		InputsHash *string         `gorm:"column:inputs_hash"`
		Digest     string          `gorm:"column:digest"`
		ReceiptID  *int64          `gorm:"column:receipt_id"`
	}
	read := r.db.WithContext(ctx).Table("story_digests").Select("article_ids, inputs_hash, digest, receipt_id").
		Where("story_id = ?", storyID).Order("version DESC").Limit(1).Scan(&row)
	if read.Error != nil {
		return nil, read.Error
	}
	if read.RowsAffected == 0 {
		return nil, nil
	}
	return &SavedDigest{ArticleIDs: []string(row.ArticleIDs), InputsHash: row.InputsHash, Digest: row.Digest, ReceiptID: row.ReceiptID}, nil
}

func (r *GormRepository) Clear(ctx context.Context, storyID int64, version int) (bool, error) {
	result := r.db.WithContext(ctx).Model(&model.Story{}).Where("id = ? AND version = ? AND merged_into IS NULL AND (digest IS NOT NULL OR latest IS NOT NULL)", storyID, version).
		Updates(map[string]any{"digest": nil, "latest": nil, "digest_updated_at": nil, "updated_at": gorm.Expr("now()")})
	return result.RowsAffected > 0, result.Error
}

func (r *GormRepository) Save(ctx context.Context, input SaveInput) (bool, error) {
	updated := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Story
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", input.StoryID).Take(&current)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return completeDigestReceipt(tx, input.ReceiptID)
		}
		if result.Error != nil {
			return result.Error
		}
		if current.MergedInto != nil || current.Version != input.StoryVersion {
			return completeDigestReceipt(tx, input.ReceiptID)
		}
		// Membership, visibility, release, evidence and corrections can change without a story version bump.
		currentReports, err := digestReports(tx, input.StoryID, time.Now())
		if err != nil {
			return err
		}
		currentHash, err := DigestInputsHash(currentReports)
		if err != nil {
			return err
		}
		if currentHash != input.InputsHash {
			return completeDigestReceipt(tx, input.ReceiptID)
		}
		latest := input.Latest
		digest := model.StoryDigest{StoryID: input.StoryID, Version: input.Version, Digest: input.Digest,
			Latest: &latest, ReceiptID: input.ReceiptID, ArticleIDs: model.TextArray(input.ArticleIDs), InputsHash: &input.InputsHash}
		if err := tx.Create(&digest).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Story{}).Where("id = ? AND version = ? AND merged_into IS NULL", input.StoryID, input.StoryVersion).Updates(map[string]any{
			"digest": input.Digest, "latest": latest, "digest_updated_at": gorm.Expr("now()"),
			"title":   gorm.Expr("CASE WHEN origin = 'manual' OR ? = '' THEN title ELSE ? END", input.Title, input.Title),
			"version": input.Version, "updated_at": gorm.Expr("now()"),
		}).Error; err != nil {
			return err
		}
		if err := completeDigestReceipt(tx, input.ReceiptID); err != nil {
			return err
		}
		updated = true
		return nil
	})
	return updated, err
}

func completeDigestReceipt(tx *gorm.DB, receiptID *int64) error {
	if receiptID == nil {
		return nil
	}
	return tx.Model(&model.PaidReceipt{}).Where("id = ?", *receiptID).Updates(map[string]any{
		"status": "completed", "completed_at": gorm.Expr("coalesce(completed_at, now())"), "updated_at": gorm.Expr("now()"),
	}).Error
}

func (r *GormRepository) StaleStories(ctx context.Context, limit int, now time.Time) ([]int64, error) {
	var ids []int64
	err := r.db.WithContext(ctx).Raw(`
		SELECT st.id FROM stories st
		WHERE st.merged_into IS NULL AND (
			(st.digest IS NULL AND EXISTS (
				SELECT 1 FROM facts f
				JOIN fact_articles fa ON fa.fact_id = f.id
				JOIN publications p ON p.article_id = fa.article_id
				WHERE f.story_id = st.id AND `+digestEvidenceSQL+` AND `+digestListedSQL+`
			))
			OR (st.digest IS NOT NULL AND st.digest_updated_at IS NULL)
			OR (st.digest_updated_at IS NOT NULL AND (
				st.updated_at > st.digest_updated_at OR EXISTS (
					SELECT 1 FROM facts f
					JOIN fact_articles fa ON fa.fact_id = f.id
					JOIN publications p ON p.article_id = fa.article_id
					WHERE f.story_id = st.id AND fa.role <> 'mention'
					  AND (p.updated_at > st.digest_updated_at OR f.updated_at > st.digest_updated_at)
				)
			))
		)
		ORDER BY coalesce(st.latest_at, st.updated_at), st.id
		LIMIT ?`, now, limit).Scan(&ids).Error
	return ids, err
}

const digestEvidenceSQL = `fa.role <> 'mention' AND NOT EXISTS (
	SELECT 1 FROM analyses evidence_an WHERE evidence_an.id = p.analysis_id AND evidence_an.output->>'scope' = 'composite'
)`

const digestListedSQL = `p.visibility = 'public' AND p.eligible AND (NOT p.selected OR p.visible_after <= ?)`

func digestReports(db *gorm.DB, storyID int64, now time.Time) ([]DigestReport, error) {
	var rows []DigestReport
	err := db.Raw(`SELECT DISTINCT ON (f.story_id, p.article_id)
		f.story_id, p.article_id AS id, p.title, p.summary, s.name AS source_name, (s.tier = 'T1') AS first_party,
		coalesce(p.published_at, p.discovered_at) AS at, f.id AS fact_id, f.subject AS fact_subject,
		f.action AS fact_action, f.object AS fact_object, f.conditions AS fact_conditions, fa.evidence,
		an.output->'fact' AS structured_fact
		FROM facts f JOIN fact_articles fa ON fa.fact_id = f.id
		JOIN publications p ON p.article_id = fa.article_id
		JOIN sources s ON s.id = p.source_id LEFT JOIN analyses an ON an.id = p.analysis_id
		WHERE f.story_id = ? AND `+digestEvidenceSQL+` AND `+digestListedSQL+`
		ORDER BY f.story_id, p.article_id, (fa.role = 'primary') DESC, f.id`, storyID, now).Scan(&rows).Error
	if rows == nil {
		rows = []DigestReport{}
	}
	return rows, err
}

var _ Repository = (*GormRepository)(nil)
