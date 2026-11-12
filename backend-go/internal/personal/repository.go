package personal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

// Repository is the GORM persistence boundary for private workspace data.
type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func scanOne(db *gorm.DB, destination any) error {
	result := db.Scan(destination)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

type ModelConfig struct {
	BaseURL          string
	APIKeyCiphertext string
	Model            string
	Version          int `gorm:"column:config_version"`
}

type Interests struct {
	Keywords  []string
	EntityIDs []string
	Version   int `gorm:"column:preference_version"`
}

type Card struct {
	ArticleID   string          `gorm:"column:article_id"`
	Score       float64         `gorm:"column:score"`
	Title       string          `gorm:"column:title"`
	Summary     string          `gorm:"column:summary"`
	Reason      *string         `gorm:"column:reason"`
	URL         string          `gorm:"column:url"`
	Source      string          `gorm:"column:source"`
	PublishedAt *time.Time      `gorm:"column:published_at"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	MediaJSON   json.RawMessage `gorm:"column:media"`
	Historical  bool            `gorm:"-"`
}

type ArticleDetail struct {
	Card          `gorm:"embedded"`
	OriginalTitle string  `gorm:"column:original_title"`
	Author        *string `gorm:"column:author"`
}

type DailyReport struct {
	ID        string          `gorm:"column:id"`
	Date      string          `gorm:"column:report_date"`
	Model     string          `gorm:"column:model"`
	Title     string          `gorm:"column:title"`
	Summary   string          `gorm:"column:summary"`
	Entries   json.RawMessage `gorm:"column:entries"`
	CreatedAt time.Time       `gorm:"column:created_at"`
}

type ReceiptStatus struct {
	Status string          `gorm:"column:status"`
	Usage  json.RawMessage `gorm:"column:usage"`
	Error  *string         `gorm:"column:error"`
}

func (r *Repository) ModelConfig(ctx context.Context, userID int64) (ModelConfig, error) {
	var value ModelConfig
	err := scanOne(r.db.WithContext(ctx).Model(&model.UserModelConfig{}).Select("base_url", "api_key_ciphertext", "model", "config_version").Where("user_id = ?", userID), &value)
	return value, err
}

func (r *Repository) SaveModelConfig(ctx context.Context, userID int64, baseURL, ciphertext, modelName string) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO user_model_configs(user_id,base_url,api_key_ciphertext,model) VALUES(?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET base_url=excluded.base_url,api_key_ciphertext=excluded.api_key_ciphertext,model=excluded.model,config_version=user_model_configs.config_version+1,updated_at=now()`, userID, baseURL, ciphertext, modelName).Error
}

func (r *Repository) Interests(ctx context.Context, userID int64) (Interests, error) {
	var value model.UserInterest
	err := scanOne(r.db.WithContext(ctx).Model(&model.UserInterest{}).Select("keywords", "entity_ids", "preference_version").Where("user_id = ?", userID), &value)
	return Interests{Keywords: value.Keywords, EntityIDs: value.EntityIDs, Version: value.PreferenceVersion}, err
}

func (r *Repository) SaveInterests(ctx context.Context, userID int64, keywords, entityIDs []string) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO user_interests(user_id,keywords,entity_ids) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET keywords=excluded.keywords,entity_ids=excluded.entity_ids,preference_version=user_interests.preference_version+1,updated_at=now()`, userID, model.TextArray(keywords), model.TextArray(entityIDs)).Error
}

func (r *Repository) StartReceipt(ctx context.Context, userID int64, purpose, articleID, modelName, logicalKey, requestHash string) (int64, error) {
	var article *string
	if articleID != "" {
		article = &articleID
	}
	receipt := model.UserModelCallReceipt{UserID: userID, LogicalKey: logicalKey, Purpose: purpose, ArticleID: article, Model: modelName, Status: "pending", RequestHash: requestHash}
	if err := r.db.WithContext(ctx).Omit("CreatedAt").Create(&receipt).Error; err != nil {
		return 0, err
	}
	return receipt.ID, nil
}

func (r *Repository) FailReceipt(ctx context.Context, id int64, message string) error {
	return r.db.WithContext(ctx).Model(&model.UserModelCallReceipt{}).Where("id = ?", id).Updates(map[string]any{
		"status":       "failed",
		"error":        message,
		"completed_at": gorm.Expr("now()"),
	}).Error
}

func (r *Repository) Receipt(ctx context.Context, id, userID int64) (ReceiptStatus, error) {
	var value ReceiptStatus
	err := scanOne(r.db.WithContext(ctx).Model(&model.UserModelCallReceipt{}).Select("status", "usage", "error").Where("id = ? AND user_id = ? AND purpose = ?", id, userID, "connection_test"), &value)
	return value, err
}

func (r *Repository) TestJobTerminal(ctx context.Context, id int64, kind string) (bool, error) {
	var terminal bool
	err := r.db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM river_job WHERE kind=? AND args->>'receiptId'=? AND state IN ('discarded','cancelled'))`, kind, strconv.FormatInt(id, 10)).Row().Scan(&terminal)
	return terminal, err
}

func (r *Repository) Feed(ctx context.Context, userID int64) ([]Card, error) {
	var items []Card
	err := r.db.WithContext(ctx).Raw(personalSelect+` WHERE r.user_id=? ORDER BY r.created_at DESC LIMIT 50`, userID).Scan(&items).Error
	if items == nil {
		items = []Card{}
	}
	return items, err
}

func (r *Repository) Hot(ctx context.Context, userID int64) ([]Card, error) {
	var items []Card
	err := r.db.WithContext(ctx).Raw(personalSelect+` WHERE r.user_id=? AND coalesce(a.published_at,a.discovered_at)>=now()-interval '48 hours' AND coalesce(a.published_at,a.discovered_at)<=now() ORDER BY r.relevance_score DESC,coalesce(a.published_at,a.discovered_at) DESC,r.article_id LIMIT 10`, userID).Scan(&items).Error
	if items == nil {
		items = []Card{}
	}
	return items, err
}

/** Article returns one personal result belonging to the requesting user. */
func (r *Repository) Article(ctx context.Context, userID int64, articleID string) (ArticleDetail, error) {
	var item ArticleDetail
	result := r.db.WithContext(ctx).Raw(`SELECT r.article_id,r.relevance_score::float8 AS score,r.title,r.summary,r.reason,a.url,s.name AS source,a.published_at,r.created_at,a.media,a.title AS original_title,a.author FROM user_article_results r JOIN articles a ON a.id=r.article_id JOIN sources s ON s.id=a.source_id WHERE r.user_id=? AND r.article_id=?`, userID, articleID).Scan(&item)
	if result.Error != nil {
		return ArticleDetail{}, result.Error
	}
	if result.RowsAffected == 0 {
		return ArticleDetail{}, ErrNotFound
	}
	return item, nil
}

func (r *Repository) Daily(ctx context.Context, userID int64) ([]DailyReport, error) {
	var items []DailyReport
	err := r.db.WithContext(ctx).Raw(`SELECT * FROM (
		SELECT 'scheduled-' || to_char(report_date,'YYYY-MM-DD') AS id,to_char(report_date,'YYYY-MM-DD') AS report_date,model,title,summary,entries,created_at FROM user_daily_reports WHERE user_id=? AND is_scheduled
		UNION ALL
		SELECT issue->>'id',issue->>'date',issue->>'model',issue->>'title',issue->>'summary',issue->'entries',(issue->>'createdAt')::timestamptz FROM user_daily_reports CROSS JOIN LATERAL jsonb_array_elements(manual_reports) issue WHERE user_id=?
	) issues ORDER BY created_at DESC,id DESC LIMIT 30`, userID, userID).Scan(&items).Error
	if items == nil {
		items = []DailyReport{}
	}
	return items, err
}

/** ManualDailyStatus reads only the requesting user's on-demand report job. */
func (r *Repository) ManualDailyStatus(ctx context.Context, userID, jobID int64) (string, error) {
	var state string
	err := r.db.WithContext(ctx).Raw(`SELECT state FROM river_job WHERE id=? AND kind='myhot_personal_daily_manual' AND args->>'userId'=?`, jobID, strconv.FormatInt(userID, 10)).Row().Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if state == "completed" {
		var count int64
		if err := r.db.WithContext(ctx).Raw(`SELECT count(*) FROM user_daily_reports CROSS JOIN LATERAL jsonb_array_elements(manual_reports) issue WHERE user_id=? AND issue->>'id'=?`, userID, "manual-"+strconv.FormatInt(jobID, 10)).Row().Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return "empty", nil
		}
		return "succeeded", nil
	}
	if state == "discarded" || state == "cancelled" {
		return "failed", nil
	}
	if state == "running" {
		return "running", nil
	}
	return "queued", nil
}

func (r *Repository) History(ctx context.Context) ([]Card, error) {
	var items []Card
	err := r.db.WithContext(ctx).Raw(`SELECT a.id AS article_id,coalesce(p.score,an.score,0)::float8 AS score,coalesce(p.title,an.title_zh,a.title) AS title,coalesce(p.summary,an.summary_zh,a.excerpt,'') AS summary,coalesce(p.reason,an.reason_zh) AS reason,a.url,s.name AS source,a.published_at,coalesce(p.updated_at,an.created_at,a.created_at) AS created_at,a.media FROM articles a JOIN sources s ON s.id=a.source_id CROSS JOIN personalization_rollout cutoff LEFT JOIN publications p ON p.article_id=a.id LEFT JOIN LATERAL (SELECT score,title_zh,summary_zh,reason_zh,created_at FROM analyses WHERE article_id=a.id ORDER BY id DESC LIMIT 1) an ON true WHERE cutoff.id=true AND a.created_at<cutoff.activated_at AND (p.visibility IS NULL OR p.visibility<>'withdrawn') ORDER BY coalesce(p.timeline_at,a.timeline_at) DESC,a.id LIMIT 100`).Scan(&items).Error
	if items == nil {
		items = []Card{}
	}
	for i := range items {
		items[i].Historical = true
	}
	return items, err
}

const personalSelect = `SELECT r.article_id,r.relevance_score::float8 AS score,r.title,r.summary,r.reason,a.url,s.name AS source,a.published_at,r.created_at,a.media FROM user_article_results r JOIN articles a ON a.id=r.article_id JOIN sources s ON s.id=a.source_id`
