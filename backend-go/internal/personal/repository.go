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
	LabelIDs  []string
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
	var value struct {
		Keywords  model.TextArray
		EntityIDs model.TextArray
		LabelIDs  model.TextArray
		Version   int
	}
	err := scanOne(r.db.WithContext(ctx).Raw(`SELECT u.keywords,u.preference_version AS version,
	ARRAY(SELECT coalesce(l.merged_into,f.id)
		FROM unnest(u.entity_ids) WITH ORDINALITY AS f(id,ord)
		LEFT JOIN interest_labels l ON l.id=f.id
		LEFT JOIN interest_labels target ON target.id=coalesce(l.merged_into,l.id)
		WHERE target.id IS NULL OR target.status IN ('active','dormant')
		GROUP BY coalesce(l.merged_into,f.id) ORDER BY min(f.ord)) AS entity_ids,
	ARRAY(SELECT DISTINCT coalesce(l.merged_into,l.id)
		FROM unnest(u.label_ids) AS f(id) JOIN interest_labels l ON l.id=f.id
		JOIN interest_labels target ON target.id=coalesce(l.merged_into,l.id)
		WHERE target.status IN ('active','dormant') ORDER BY 1) AS label_ids
	FROM user_interests u WHERE u.user_id=?`, userID), &value)
	if err != nil {
		return Interests{}, err
	}
	return Interests{
		Keywords:  value.Keywords,
		EntityIDs: value.EntityIDs,
		LabelIDs:  append([]string{}, value.LabelIDs...),
		Version:   value.Version,
	}, nil
}

func (r *Repository) SaveInterests(ctx context.Context, userID int64, keywords, entityIDs []string) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO user_interests(user_id,keywords,entity_ids) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET keywords=excluded.keywords,entity_ids=excluded.entity_ids,preference_version=user_interests.preference_version+1,updated_at=now()`, userID, model.TextArray(keywords), model.TextArray(entityIDs)).Error
}

/** SaveInterestsWithLabels replaces a user's label choices after validating stable IDs. */
func (r *Repository) SaveInterestsWithLabels(ctx context.Context, userID int64, keywords, entities, labels []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('interest-label-catalog'))").Error; err != nil {
			return err
		}
		var valid []string
		if err := tx.Raw(`SELECT DISTINCT l.id FROM interest_labels l WHERE l.id=ANY(?::text[])
    AND (l.status='active' OR (l.status='dormant' AND EXISTS(
      SELECT 1 FROM user_interests u JOIN interest_labels prev ON prev.id=ANY(u.label_ids)
      WHERE u.user_id=? AND coalesce(prev.merged_into,prev.id)=l.id)))`, model.TextArray(labels), userID).Scan(&valid).Error; err != nil {
			return err
		}
		if len(valid) != len(labels) {
			return &InputError{Message: "只能选择可用的稳定标签"}
		}
		return tx.Exec(`INSERT INTO user_interests(user_id,keywords,entity_ids,label_ids) VALUES(?,?,?,?)
    ON CONFLICT(user_id) DO UPDATE SET keywords=excluded.keywords,entity_ids=excluded.entity_ids,
    label_ids=excluded.label_ids,preference_version=user_interests.preference_version+1,updated_at=now()`,
			userID, model.TextArray(keywords), model.TextArray(entities), model.TextArray(labels)).Error
	})
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

/** Hot reads the user's ranked results within an inclusive start and exclusive end. */
func (r *Repository) Hot(ctx context.Context, userID int64, window hotWindow) ([]Card, error) {
	var items []Card
	err := r.db.WithContext(ctx).Raw(personalSelect+` WHERE r.user_id=? AND coalesce(a.published_at,a.discovered_at)>=? AND coalesce(a.published_at,a.discovered_at)<? ORDER BY r.relevance_score DESC,coalesce(a.published_at,a.discovered_at) DESC,r.article_id LIMIT ? OFFSET ?`, userID, window.Start, window.End, window.Limit+1, window.Offset).Scan(&items).Error
	if items == nil {
		items = []Card{}
	}
	return items, err
}

/** Article returns one personal result belonging to the requesting user. */
func (r *Repository) Article(ctx context.Context, userID int64, articleID string) (ArticleDetail, error) {
	var item ArticleDetail
	result := r.db.WithContext(ctx).Raw(`SELECT r.article_id,r.relevance_score::float8 AS score,r.title,r.summary,r.reason,a.url,coalesce(sub.name,s.name) AS source,a.published_at,r.created_at,a.media,a.title AS original_title,a.author FROM user_article_results r JOIN articles a ON a.id=r.article_id JOIN sources s ON s.id=a.source_id LEFT JOIN user_source_subscriptions sub ON sub.user_id=r.user_id AND sub.source_id=s.id WHERE r.user_id=? AND r.article_id=?`, userID, articleID).Scan(&item)
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

type FunnelItem struct {
	ArticleID   string     `gorm:"column:article_id" json:"articleId"`
	Title       string     `gorm:"column:title" json:"title"`
	Source      string     `gorm:"column:source" json:"source"`
	URL         string     `gorm:"column:url" json:"url"`
	PublishedAt *time.Time `gorm:"column:published_at" json:"publishedAt"`
	CreatedAt   time.Time  `gorm:"column:created_at" json:"createdAt"`
}

type FunnelCounts struct {
	TotalScanned  int64 `gorm:"column:total_scanned"`
	PassedCount   int64 `gorm:"column:passed_count"`
	FilteredCount int64 `gorm:"column:filtered_count"`
}

type FunnelView struct {
	TotalScanned  int64        `json:"totalScanned"`
	PassedCount   int64        `json:"passedCount"`
	FilteredCount int64        `json:"filteredCount"`
	FilteredItems []FunnelItem `json:"filteredItems"`
}

/** Funnel calculates today's scan, pass, and filter metrics and lists recent filtered articles. */
func (r *Repository) Funnel(ctx context.Context, userID int64) (FunnelView, error) {
	var counts FunnelCounts

	// 统计今日扫描、入库和被过滤数量
	countQuery := `WITH today_scanned AS (
		SELECT DISTINCT a.id AS article_id
		FROM articles a
		JOIN article_discoveries ad ON ad.article_id = a.id
		JOIN user_source_subscriptions sub ON sub.source_id = ad.source_id AND sub.enabled
		WHERE sub.user_id = ?
		  AND ad.discovered_at >= ((now() AT TIME ZONE 'Asia/Shanghai')::date::timestamp AT TIME ZONE 'Asia/Shanghai')
		  AND ad.discovered_at <= now()
	),
	user_evaluated AS (
		SELECT article_id FROM user_article_results WHERE user_id = ?
	)
	SELECT 
		(SELECT count(*) FROM today_scanned) AS total_scanned,
		(SELECT count(*) FROM today_scanned t JOIN user_evaluated e ON e.article_id = t.article_id) AS passed_count,
		(SELECT count(*) FROM today_scanned t WHERE NOT EXISTS (SELECT 1 FROM user_evaluated e WHERE e.article_id = t.article_id)) AS filtered_count`

	if err := r.db.WithContext(ctx).Raw(countQuery, userID, userID).Scan(&counts).Error; err != nil {
		return FunnelView{}, err
	}

	var items []FunnelItem

	// 查询被过滤的最近文章列表
	itemsQuery := `WITH today_scanned AS (
		SELECT DISTINCT a.id AS article_id, a.title, s.name AS source, a.url, a.published_at, ad.discovered_at AS created_at
		FROM articles a
		JOIN article_discoveries ad ON ad.article_id = a.id
		JOIN user_source_subscriptions sub ON sub.source_id = ad.source_id AND sub.enabled
		JOIN sources s ON s.id = ad.source_id
		WHERE sub.user_id = ?
		  AND ad.discovered_at >= ((now() AT TIME ZONE 'Asia/Shanghai')::date::timestamp AT TIME ZONE 'Asia/Shanghai')
		  AND ad.discovered_at <= now()
	)
	SELECT t.article_id, t.title, t.source, t.url, t.published_at, t.created_at
	FROM today_scanned t
	WHERE NOT EXISTS (SELECT 1 FROM user_article_results e WHERE e.user_id = ? AND e.article_id = t.article_id)
	ORDER BY t.created_at DESC
	LIMIT 20`

	if err := r.db.WithContext(ctx).Raw(itemsQuery, userID, userID).Scan(&items).Error; err != nil {
		return FunnelView{}, err
	}

	if items == nil {
		items = []FunnelItem{}
	}

	return FunnelView{
		TotalScanned:  counts.TotalScanned,
		PassedCount:   counts.PassedCount,
		FilteredCount: counts.FilteredCount,
		FilteredItems: items,
	}, nil
}

const personalSelect = `SELECT r.article_id,r.relevance_score::float8 AS score,r.title,r.summary,r.reason,a.url,coalesce(sub.name,s.name) AS source,a.published_at,r.created_at,a.media FROM user_article_results r JOIN articles a ON a.id=r.article_id JOIN sources s ON s.id=a.source_id LEFT JOIN user_source_subscriptions sub ON sub.user_id=r.user_id AND sub.source_id=s.id`
