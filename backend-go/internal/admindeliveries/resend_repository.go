package admindeliveries

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *GormRepository) LoadResend(ctx context.Context, id int64) (*ResendRecord, bool, error) {
	var row struct {
		Status      string          `gorm:"column:status"`
		Version     string          `gorm:"column:version"`
		Payload     json.RawMessage `gorm:"column:payload;type:jsonb"`
		TargetKey   string          `gorm:"column:target_key"`
		ConfigRef   *string         `gorm:"column:config_ref"`
		Kind        string          `gorm:"column:kind"`
		Enabled     bool            `gorm:"column:enabled"`
		EnabledAt   *time.Time      `gorm:"column:enabled_at"`
		SubjectKind string          `gorm:"column:subject_kind"`
		SubjectID   string          `gorm:"column:subject_id"`
	}
	result := r.db.WithContext(ctx).Table("deliveries AS d").Select(`
		d.status, d.updated_at::text AS version, d.payload, d.target_key, t.config_ref, t.kind, t.enabled, t.enabled_at,
		d.subject_kind, d.subject_id`).Joins("JOIN notify_targets AS t ON t.key = d.target_key").Where("d.id = ?", id).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if result.Error != nil {
		return nil, false, result.Error
	}
	configRef := ""
	if row.ConfigRef != nil {
		configRef = *row.ConfigRef
	}
	return &ResendRecord{
		Status: row.Status, Version: row.Version, Payload: row.Payload, TargetKey: row.TargetKey,
		ConfigRef: configRef, Kind: row.Kind, Enabled: row.Enabled, EnabledAt: row.EnabledAt,
		SubjectKind: row.SubjectKind, SubjectID: row.SubjectID,
	}, true, nil
}

func (r *GormRepository) LoadSelectedContent(ctx context.Context, articleID string) (*SelectedContent, error) {
	var row SelectedContent
	result := r.db.WithContext(ctx).Table("publications AS p").Select(`
		p.article_id, p.selected, p.visibility, p.title, p.summary, p.reason, p.category, p.score,
		s.name AS source_name, s.tier AS source_tier, p.url, p.timeline_at, p.published_at, p.discovered_at, p.backfill,
		p.fact_id, coalesce((o.fields->>'silent')::boolean, false) AS silent`).
		Joins("JOIN sources AS s ON s.id = p.source_id").
		Joins("LEFT JOIN editorial_overrides AS o ON o.article_id = p.article_id").
		Where("p.article_id = ?", articleID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	return &row, nil
}

func (r *GormRepository) AcquireTitleLease(ctx context.Context, leaseKey, holder string, expiresAt time.Time) (string, error) {
	var current string
	err := r.db.WithContext(ctx).Raw(`INSERT INTO delivery_leases (lease_key, holder, expires_at)
		VALUES (?, ?, ?) ON CONFLICT (lease_key) DO UPDATE SET
		holder = CASE WHEN delivery_leases.expires_at < now() THEN EXCLUDED.holder ELSE delivery_leases.holder END,
		expires_at = CASE WHEN delivery_leases.expires_at < now() THEN EXCLUDED.expires_at ELSE delivery_leases.expires_at END
		RETURNING holder`, leaseKey, holder, expiresAt).Row().Scan(&current)
	return current, err
}

func (r *GormRepository) SelectedSiblings(ctx context.Context, factID int64, articleID string) ([]string, error) {
	var rows []struct {
		ArticleID string `gorm:"column:article_id"`
	}
	err := r.db.WithContext(ctx).Raw(`SELECT article_id FROM fact_articles WHERE fact_id = ? AND article_id <> ? ORDER BY article_id`, factID, articleID).Scan(&rows).Error
	if rows == nil {
		rows = []struct {
			ArticleID string `gorm:"column:article_id"`
		}{}
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ArticleID)
	}
	return ids, err
}

func (r *GormRepository) ContentTargets(ctx context.Context) ([]ContentTarget, error) {
	var rows []ContentTarget
	err := r.db.WithContext(ctx).Table("notify_targets").Select(`key, kind, coalesce(config_ref, '') AS config_ref`).
		Where("purpose = 'content' AND enabled").Order("key").Scan(&rows).Error
	if rows == nil {
		rows = []ContentTarget{}
	}
	return rows, err
}

func (r *GormRepository) ClaimContentDelivery(ctx context.Context, targetKey string, claim ContentDeliveryClaim) (int64, ContentTarget, bool, error) {
	var id int64
	var target ContentTarget
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var selected struct {
			Key       string     `gorm:"column:key"`
			Kind      string     `gorm:"column:kind"`
			ConfigRef *string    `gorm:"column:config_ref"`
			EnabledAt *time.Time `gorm:"column:enabled_at"`
		}
		locked := tx.Table("notify_targets").Select("key, kind, config_ref, enabled_at").
			Where("key = ? AND purpose = 'content' AND enabled", targetKey).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&selected)
		if errors.Is(locked.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if locked.Error != nil {
			return locked.Error
		}
		if selected.EnabledAt != nil && claim.ContentAt.Before(*selected.EnabledAt) {
			return nil
		}
		if len(claim.SentSubjects) > 0 {
			var existing int64
			if err := tx.Table("deliveries").Where(`target_key = ? AND subject_kind = 'selected' AND subject_id IN ? AND status IN ?`,
				selected.Key, claim.SentSubjects, []string{"pending", "sent", "unknown", "sending"}).Limit(1).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				return nil
			}
		}
		payload, err := json.Marshal(claim.Card)
		if err != nil {
			return err
		}
		row := tx.Raw(`INSERT INTO deliveries (target_key, subject_kind, subject_id, dedupe_key, status, payload)
			VALUES (?, 'selected', ?, ?, 'pending', ?::jsonb) ON CONFLICT (target_key, dedupe_key) DO NOTHING RETURNING id`,
			selected.Key, claim.ArticleID, claim.DedupeKey, string(payload)).Row()
		if err := row.Scan(&id); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		configRef := ""
		if selected.ConfigRef != nil {
			configRef = *selected.ConfigRef
		}
		target = ContentTarget{Key: selected.Key, Kind: selected.Kind, ConfigRef: configRef}
		return nil
	})
	return id, target, id > 0, err
}

func (r *GormRepository) StartContentDelivery(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Table("deliveries").Where("id = ? AND status = 'pending'", id).
		Updates(map[string]any{"status": "sending", "attempts": gorm.Expr("attempts + 1"), "updated_at": gorm.Expr("now()")}).Error
}

func (r *GormRepository) FinishContentDelivery(ctx context.Context, id int64, status, response string) error {
	updates := map[string]any{"status": status, "response": response, "updated_at": gorm.Expr("now()")}
	if status == "sent" {
		updates["sent_at"] = gorm.Expr("now()")
	} else {
		updates["sent_at"] = nil
	}
	return r.db.WithContext(ctx).Table("deliveries").Where("id = ? AND status IN ?", id, []string{"pending", "sending"}).Updates(updates).Error
}

func (r *GormRepository) ClaimResend(ctx context.Context, id int64, version string, payload json.RawMessage) (string, bool, error) {
	var claimedVersion string
	result := r.db.WithContext(ctx).Raw(`UPDATE deliveries SET status = 'sending', attempts = attempts + 1,
		payload = ?::jsonb, updated_at = now()
		WHERE id = ? AND status IN ('unknown', 'failed') AND updated_at::text = ?
		RETURNING updated_at::text`, string(payload), id, version).Row()
	err := result.Scan(&claimedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return claimedVersion, true, nil
}

func (r *GormRepository) FinishResend(ctx context.Context, id int64, claimVersion, beforeStatus, status, response, actor, note string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"status": status, "response": response, "updated_at": gorm.Expr("now()")}
		if status == "sent" {
			updates["sent_at"] = gorm.Expr("now()")
		} else {
			updates["sent_at"] = nil
		}
		updated := tx.Model(&model.Delivery{}).Where("id = ? AND status = 'sending' AND updated_at::text = ?", id, claimVersion).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return ErrChanged
		}
		beforeJSON, err := json.Marshal(map[string]string{"status": beforeStatus})
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(map[string]string{"status": status})
		if err != nil {
			return err
		}
		subject := fmt.Sprintf("delivery:%d", id)
		return tx.Create(&model.AdminAuditLog{
			Actor: actor, Action: "delivery.resend", Subject: &subject, Reason: &note,
			Before: beforeJSON, After: afterJSON,
		}).Error
	})
}

var _ ResendRepository = (*GormRepository)(nil)
