package sourcenaming

import (
	"context"
	"encoding/json"

	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/sourceschedule"

	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

type namingSource struct {
	Status            string
	UserID            *int64
	Candidate         string
	URL               string
	Title             string
	BaseURL           string
	Model             string
	Cipher            string
	PlatformReceiptID int64
	PlatformResponse  string
}

/** NewRepository provides storage for source naming. */
func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

/** Register records the creator in the transaction that inserts the source. */
func Register(tx *gorm.DB, id string, userID int64) error {
	return tx.Exec(`INSERT INTO source_naming(source_id,user_id,status) VALUES(?,?,'awaiting_fetch')`, id, userID).Error
}

/** RecordCollection makes a newly collected source or a legacy RSS placeholder eligible. */
func RecordCollection(tx *gorm.DB, id string, userID int64) error {
	return tx.Exec(`INSERT INTO source_naming(source_id,user_id,status)
		SELECT id, (SELECT id FROM user_accounts WHERE id=? AND verified_at IS NOT NULL), 'ready'
		FROM sources WHERE id=? AND kind IN ('rss','web_list')
		  AND (EXISTS (SELECT 1 FROM source_naming WHERE source_id=sources.id)
		       OR (kind='rss' AND regexp_replace(lower(name), '[[:space:]]+', '', 'g')='个人rss'))
		ON CONFLICT (source_id) DO UPDATE SET status='ready' WHERE source_naming.status='awaiting_fetch'`, userID, id).Error
}

/** ReadyIDs returns names to start or publish, avoiding repeated polling of missing credentials. */
func (r *Repository) ReadyIDs(ctx context.Context, sourceID string, fallbackConfigured bool) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Raw(`SELECT n.source_id FROM source_naming n
		WHERE (?='' OR n.source_id=?) AND (n.status='received'
		  OR (n.status='calling' AND EXISTS (SELECT 1 FROM receipts p WHERE p.logical_key=n.platform_receipt_key AND p.status IN ('received','completed')))
		  OR (n.status='ready' AND (? OR EXISTS (
			SELECT 1 FROM user_model_configs m JOIN user_accounts u ON u.id=m.user_id
			WHERE m.user_id=n.user_id AND u.verified_at IS NOT NULL))))
		ORDER BY n.source_id LIMIT 40`, sourceID, sourceID, fallbackConfigured).Scan(&ids).Error
	return ids, err
}

/** source returns the stable naming owner and the owner's current verified model configuration. */
func (r *Repository) source(ctx context.Context, id string) (namingSource, error) {
	var src namingSource
	err := r.db.WithContext(ctx).Raw(`SELECT n.status,n.user_id,coalesce(n.candidate,'') AS candidate,
		coalesce(s.config->>'feedUrl',s.config->>'url','') AS url,coalesce(s.cursor->>'rssTitle','') AS title,
		coalesce(m.base_url,'') AS base_url,coalesce(m.model,'') AS model,coalesce(m.api_key_ciphertext,'') AS cipher,
		coalesce(p.id,0) AS platform_receipt_id,coalesce(p.response::text,'') AS platform_response
		FROM source_naming n JOIN sources s ON s.id=n.source_id
		LEFT JOIN user_accounts u ON u.id=n.user_id AND u.verified_at IS NOT NULL
		LEFT JOIN user_model_configs m ON m.user_id=u.id
		LEFT JOIN receipts p ON p.logical_key=n.platform_receipt_key AND p.status IN ('received','completed')
		WHERE n.source_id=?`, id).Scan(&src).Error
	return src, err
}

/** samples loads a bounded set of articles discovered by this source. */
func (r *Repository) samples(ctx context.Context, id string) ([]articleSample, error) {
	var samples []articleSample
	err := r.db.WithContext(ctx).Raw(`SELECT left(a.title,160) AS title,left(coalesce(a.excerpt,''),240) AS excerpt
		FROM article_discoveries d JOIN articles a ON a.id=d.article_id
		WHERE d.source_id=? ORDER BY d.discovered_at DESC,a.id LIMIT 5`, id).Scan(&samples).Error
	return samples, err
}

/** claim grants one attempt and records a personal receipt before dispatch. */
func (r *Repository) claim(ctx context.Context, id string, userID *int64, modelName, requestHash, platformKey string) (bool, int64, error) {
	var claimed bool
	var receiptID int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE source_naming SET status='calling',platform_receipt_key=NULLIF(?,'') WHERE source_id=? AND status='ready'`, platformKey, id)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		claimed = true
		if userID == nil {
			return nil
		}
		return tx.Raw(`INSERT INTO user_model_call_receipts(user_id,logical_key,purpose,model,status,request_hash)
			VALUES(?,?,'source_naming',?,'pending',?) RETURNING id`, *userID, "source_naming:"+id, modelName, requestHash).Row().Scan(&receiptID)
	})
	return claimed, receiptID, err
}

/** recordResult stores the model result before attempting to publish the name. */
func (r *Repository) recordResult(ctx context.Context, id, candidate, failure string, receiptID int64, usage json.RawMessage) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		status := "received"
		receiptStatus := "completed"
		if failure != "" {
			status = "failed"
			receiptStatus = "failed"
		}
		if err := tx.Exec(`UPDATE source_naming SET status=?,candidate=NULLIF(?,'') WHERE source_id=? AND status='calling'`, status, candidate, id).Error; err != nil {
			return err
		}
		if receiptID == 0 {
			return nil
		}
		return tx.Exec(`UPDATE user_model_call_receipts SET status=?,usage=?::jsonb,error=NULLIF(?,''),completed_at=now()
			WHERE id=? AND status='pending'`, receiptStatus, string(usage), failure, receiptID).Error
	})
}

/** ReserveName serializes shared name allocation and checks all existing sources. */
func ReserveName(tx *gorm.DB, id, candidate, qualifier string) (string, error) {
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('myhot:shared-source-name'))`).Error; err != nil {
		return "", err
	}
	for attempt := 0; ; attempt++ {
		name := distinctName(candidate, qualifier, id, attempt)
		var exists bool
		err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM sources WHERE id<>? AND lower(regexp_replace(name, '^[[:space:]]+|[[:space:]]+$', '', 'g'))=lower(?::text))`, id, name).Row().Scan(&exists)
		if err != nil || !exists {
			return name, err
		}
	}
}

/** publish saves the unique shared name and refreshes inherited subscriptions atomically. */
func (r *Repository) publish(ctx context.Context, id string, src namingSource) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		name, err := ReserveName(tx, id, src.Candidate, qualifier(src.URL))
		if err != nil {
			return err
		}
		if err := sourceschedule.Lock(tx, id); err != nil {
			return err
		}
		result := tx.Exec(`UPDATE source_naming SET status='completed',candidate=NULL WHERE source_id=? AND status='received'`, id)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if err := tx.Model(&model.Source{}).Where("id = ?", id).Updates(map[string]any{
			"name":       name,
			"updated_at": gorm.Expr("now()"),
		}).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE user_source_subscriptions SET name=?, updated_at=now() WHERE source_id=? AND name_inherited`, name, id).Error
	})
}
