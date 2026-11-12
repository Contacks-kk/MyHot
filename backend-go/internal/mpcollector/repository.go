package mpcollector

import (
	"context"
	"encoding/json"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) DueSources(ctx context.Context) ([]Source, error) {
	var rows []Source
	err := r.db.WithContext(ctx).Table("sources").Select(`id, name, config, cursor, enabled, participation_mode,
		interval_minutes, fail_count, (cursor->>'lastCheckedAt' IS NULL) AS first_check`).
		Where("kind = 'mp_account' AND enabled AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id AND u.enabled)").Order("next_fetch_at NULLS FIRST, id").Scan(&rows).Error
	if rows == nil {
		rows = []Source{}
	}
	return rows, err
}

func (r *GormRepository) Begin(ctx context.Context, sourceID, reason string) (*Run, bool, error) {
	var source Source
	result := r.db.WithContext(ctx).Table("sources").Select(`id, name, config, cursor, enabled, participation_mode,
		interval_minutes, fail_count, (cursor->>'lastCheckedAt' IS NULL) AS first_check`).
		Where("id = ? AND kind = 'mp_account' AND enabled AND EXISTS (SELECT 1 FROM user_source_subscriptions u WHERE u.source_id=sources.id AND u.enabled)", sourceID).Take(&source)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, false, nil
		}
		return nil, false, result.Error
	}
	detail, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return nil, false, err
	}
	run := model.FetchRun{SourceID: sourceID, Status: "running", Detail: detail}
	if err := r.db.WithContext(ctx).Create(&run).Error; err != nil {
		return nil, false, err
	}
	return &Run{ID: run.ID, Source: source}, true, nil
}

func (r *GormRepository) KnownArticle(ctx context.Context, identity string) (*KnownArticle, error) {
	var result struct {
		ID           string          `gorm:"column:id"`
		BodyStatus   string          `gorm:"column:body_status"`
		DiscoveredAt time.Time       `gorm:"column:discovered_at"`
		Retry        json.RawMessage `gorm:"column:retry"`
	}
	err := r.db.WithContext(ctx).Table("articles").Select(`id, body_status, discovered_at, raw->'dajiala'->'bodyRetry' AS retry`).
		Where("identity_key = ?", identity).Take(&result).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	known := &KnownArticle{ID: result.ID, BodyStatus: result.BodyStatus, DiscoveredAt: result.DiscoveredAt}
	if len(result.Retry) > 0 && string(result.Retry) != "null" {
		var retry BodyRetry
		if err := json.Unmarshal(result.Retry, &retry); err == nil {
			known.Retry = &retry
		}
	}
	return known, nil
}

func (r *GormRepository) UpdateBodyRetry(ctx context.Context, articleID string, retry BodyRetry) error {
	encoded, err := json.Marshal(retry)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Exec(`UPDATE articles SET raw = jsonb_set(
		CASE WHEN jsonb_typeof(raw) = 'object' THEN raw ELSE '{}'::jsonb END,
		'{dajiala,bodyRetry}', ?::jsonb, true) WHERE id = ?`, string(encoded), articleID).Error
}

func (r *GormRepository) ClearBodyRetry(ctx context.Context, articleID string) error {
	return r.db.WithContext(ctx).Exec(`UPDATE articles SET raw = coalesce(raw, '{}'::jsonb) #- '{dajiala,bodyRetry}' WHERE id = ?`, articleID).Error
}

func (r *GormRepository) FinishSuccess(ctx context.Context, run *Run, found, created int, detail json.RawMessage) error {
	now := time.Now().UTC()
	if err := r.db.WithContext(ctx).Model(&model.Source{}).Where("id = ?", run.Source.ID).Updates(map[string]any{
		"last_fetch_at": now, "last_ok_at": now, "fail_count": 0, "last_error": nil, "health": "ok",
		"cursor": run.Source.Cursor, "next_fetch_at": gorm.Expr("now() + make_interval(mins => interval_minutes)"), "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&model.FetchRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": "ok", "finished_at": now, "found_count": found, "new_count": created, "detail": nullableJSON(detail),
	}).Error
}

func (r *GormRepository) FinishFailure(ctx context.Context, run *Run, found, created int, soft bool, message string) error {
	now := time.Now().UTC()
	if err := r.db.WithContext(ctx).Model(&model.Source{}).Where("id = ?", run.Source.ID).Updates(map[string]any{
		"last_fetch_at": now,
		"fail_count":    gorm.Expr("CASE WHEN ? THEN fail_count ELSE fail_count + 1 END", soft),
		"last_error":    message,
		"health":        gorm.Expr("CASE WHEN ? THEN health WHEN fail_count + 1 >= 3 THEN 'failing' ELSE 'degraded' END", soft),
		"updated_at":    now,
	}).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&model.FetchRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": "failed", "finished_at": now, "found_count": found, "new_count": created, "error": message,
	}).Error
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

var _ Repository = (*GormRepository)(nil)
