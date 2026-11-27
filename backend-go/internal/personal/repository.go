package personal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/publiccontent"
	"aihot/backend-go/internal/sourcenaming"
	"aihot/backend-go/internal/sourceschedule"
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
	ArticleID     string          `gorm:"column:article_id"`
	Score         float64         `gorm:"column:score"`
	Title         string          `gorm:"column:title"`
	Summary       string          `gorm:"column:summary"`
	Reason        *string         `gorm:"column:reason"`
	URL           string          `gorm:"column:url"`
	Source        string          `gorm:"column:source"`
	SourceID      string          `gorm:"column:source_id"`
	SourceIconURL *string         `gorm:"column:source_icon_url"`
	IsSubscribed  bool            `gorm:"column:is_subscribed"`
	PublishedAt   *time.Time      `gorm:"column:published_at"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
	MediaJSON     json.RawMessage `gorm:"column:media"`
	Historical    bool            `gorm:"-"`
}

type RecommendedArticleItem struct {
	ArticleID      string     `gorm:"column:article_id" json:"articleId"`
	IdentityKey    string     `gorm:"column:identity_key" json:"-"`
	Fingerprint    *string    `gorm:"column:fingerprint" json:"-"`
	Score          *float64   `gorm:"column:score" json:"score"`
	Title          string     `gorm:"column:title" json:"title"`
	Summary        string     `gorm:"column:summary" json:"summary"`
	URL            string     `gorm:"column:url" json:"url"`
	SourceID       string     `gorm:"column:source_id" json:"sourceId"`
	SourceName     string     `gorm:"column:source_name" json:"sourceName"`
	SourceKind     string     `gorm:"column:source_kind" json:"sourceKind"`
	SourceIconURL  *string    `gorm:"column:source_icon_url" json:"-"`
	IsSubscribed   bool       `gorm:"column:is_subscribed" json:"isSubscribed"`
	PublishedAt    *time.Time `gorm:"column:published_at" json:"publishedAt"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"createdAt"`
	MatchedTagsRaw string     `gorm:"column:matched_tags_raw" json:"-"`
	MatchedTags    []string   `gorm:"-" json:"matchedTags"`
}

type ArticleDetail struct {
	Card          `gorm:"embedded"`
	OriginalTitle string  `gorm:"column:original_title"`
	Author        *string `gorm:"column:author"`
}

type DailyReport struct {
	ID        string          `gorm:"column:id" json:"id"`
	Date      string          `gorm:"column:report_date" json:"date"`
	Model     string          `gorm:"column:model" json:"model"`
	Title     string          `gorm:"column:title" json:"title"`
	Summary   string          `gorm:"column:summary" json:"summary"`
	Entries   json.RawMessage `gorm:"column:entries" json:"entries"`
	CreatedAt time.Time       `gorm:"column:created_at" json:"createdAt"`
}

type DailyReportHeading struct {
	ID        string    `json:"id"`
	Date      string    `gorm:"column:report_date" json:"date"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

type DailyReportView struct {
	Reports []DailyReportHeading `json:"reports"`
	Report  *DailyReport         `json:"report"`
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

/** Article returns only a result belonging to the requesting user. */
func (r *Repository) Article(ctx context.Context, userID int64, articleID string) (ArticleDetail, error) {
	var item ArticleDetail
	err := scanOne(r.db.WithContext(ctx).Raw(`SELECT r.article_id,s.id AS source_id,(coalesce(sub.id,0)>0) AS is_subscribed,r.relevance_score::float8 AS score,r.title,r.summary,r.reason,a.url,coalesce(sub.name,s.name) AS source,a.published_at,r.created_at,a.media,a.title AS original_title,a.author FROM user_article_results r JOIN articles a ON a.id=r.article_id JOIN sources s ON s.id=a.source_id LEFT JOIN user_source_subscriptions sub ON sub.user_id=r.user_id AND sub.source_id=s.id WHERE r.user_id=? AND r.article_id=?`, userID, articleID), &item)
	return item, err
}

const recommendationLimit = 6
const recommendationSourceLimit = 2

// recommendationFingerprint accepts content hashes backed by more than a title.
const recommendationFingerprint = `CASE WHEN a.content_hash ~ '^[0-9a-f]{64}$'
	AND (nullif(btrim(a.excerpt),'') IS NOT NULL OR nullif(btrim(a.body_text),'') IS NOT NULL)
	THEN a.content_hash END`

const recommendationBatchSize = 24

type recommendationCandidate struct {
	ArticleID   string
	IdentityKey string
	Fingerprint *string
	URL         string
	SourceID    string
}

const recommendationCandidatesSQL = `WITH followed_tags AS (
		SELECT DISTINCT target.id,target.name FROM user_interests u
		CROSS JOIN LATERAL unnest(coalesce(u.entity_ids,'{}'::text[]) || coalesce(u.label_ids,'{}'::text[])) followed(id)
		LEFT JOIN interest_labels alias ON alias.id=followed.id AND alias.status='merged'
		JOIN interest_labels target ON target.id=coalesce(alias.merged_into,followed.id)
			AND target.status IN ('active','dormant')
		WHERE u.user_id=?
	), owned AS MATERIALIZED (
		SELECT a.id,a.identity_key,a.url,` + recommendationFingerprint + ` AS fingerprint
		FROM user_article_results r JOIN articles a ON a.id=r.article_id WHERE r.user_id=?
	), candidates AS MATERIALIZED (
		SELECT a.id,a.identity_key,` + recommendationFingerprint + ` AS fingerprint,
			a.title,a.excerpt,a.url,a.published_at,a.discovered_at,
			s.id AS source_id,s.name AS source_name,s.kind AS source_kind,s.icon_url AS source_icon_url,
			(SELECT count(*) FROM user_source_subscriptions us WHERE us.source_id=s.id AND us.enabled) AS subscriber_count,
			EXISTS (SELECT 1 FROM user_source_subscriptions us WHERE us.source_id=s.id AND us.user_id=? AND us.enabled) AS is_subscribed,
			tags.names,tags.matches
		FROM articles a JOIN sources s ON s.id=a.source_id
		JOIN LATERAL (
			SELECT jsonb_agg(DISTINCT ft.name ORDER BY ft.name)::text AS names,count(DISTINCT ft.id) AS matches
			FROM article_interest_labels al JOIN followed_tags ft ON ft.id=al.label_id WHERE al.article_id=a.id
		) tags ON tags.matches>0
		WHERE s.health<>'failing' AND %s
		  AND ` + sourceschedule.RecommendationArticleWindow + `%s
	)`

const recommendationFilterSQL = `FROM candidates c LEFT JOIN (?) p ON p.article_id=c.id
	WHERE NOT coalesce(p.blocked,false)
	  AND ((p.public_detail AND p.score IS NOT NULL AND nullif(btrim(p.summary),'') IS NOT NULL)
		OR nullif(btrim(c.excerpt),'') IS NOT NULL)
	  AND NOT EXISTS (SELECT 1 FROM owned o
		WHERE o.id=c.id OR o.identity_key=c.identity_key OR o.url=c.url OR o.fingerprint=c.fingerprint)
	ORDER BY (CASE WHEN p.public_detail AND p.score IS NOT NULL THEN least(100,p.score + c.matches*2)::float8 END) DESC NULLS LAST,(CASE WHEN p.public_detail AND p.score IS NOT NULL THEN 0 ELSE c.matches END) DESC,
		c.subscriber_count DESC,coalesce(c.published_at,c.discovered_at) DESC,c.id ASC`

const recommendationCardSQL = `SELECT c.id AS article_id,c.identity_key,c.fingerprint,c.url,c.source_id,c.source_name,c.source_kind,
		c.source_icon_url,c.published_at,c.discovered_at AS created_at,c.is_subscribed,c.names AS matched_tags_raw,
		CASE WHEN p.public_detail AND p.score IS NOT NULL THEN least(100,p.score + c.matches*2)::float8 END AS score,
		CASE WHEN p.public_detail AND p.score IS NOT NULL THEN p.title ELSE c.title END AS title,
		CASE WHEN p.public_detail AND p.score IS NOT NULL THEN coalesce(nullif(btrim(p.summary),''),c.excerpt) ELSE c.excerpt END AS summary`

/** recommendationQuery applies the shared eligibility rules and deterministic ranking. */
func recommendationQuery(db *gorm.DB, userID int64, feeds []string, projection string, ids []string) (string, []any) {
	args := []any{userID, userID, userID, model.TextArray(feeds)}
	restrict := ""
	if len(ids) > 0 {
		restrict = " AND a.id IN ?"
		args = append(args, ids)
	}
	args = append(args, publiccontent.RecommendationPublications(db))
	query := fmt.Sprintf(recommendationCandidatesSQL, sourceschedule.RecommendationFeedSQL("s"), restrict) +
		" " + projection + " " + recommendationFilterSQL
	return query, args
}

/** RecommendedArticles collects one page and hydrates only its selected cards from the same snapshot. */
func (r *Repository) RecommendedArticles(ctx context.Context, userID int64, feeds []string, page int) ([]RecommendedArticleItem, bool, error) {
	items := make([]RecommendedArticleItem, 0, recommendationLimit)
	if len(feeds) == 0 {
		return items, false, nil
	}
	var hasMore bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ids, more, err := recommendationPageIDs(tx, userID, feeds, page)
		hasMore = more
		if err != nil || len(ids) == 0 {
			return err
		}

		// The same ranking orders these hydrated cards the same way as the candidate scan.
		query, args := recommendationQuery(tx, userID, feeds, recommendationCardSQL, ids)
		if err := tx.Raw(query, args...).Scan(&items).Error; err != nil {
			return err
		}
		for i := range items {
			item := &items[i]
			if err := json.Unmarshal([]byte(item.MatchedTagsRaw), &item.MatchedTags); err != nil {
				return err
			}
			item.SourceName = sourcenaming.LegacyDisplayName(item.SourceKind, item.SourceName, item.SourceID, item.URL)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return items, hasMore, err
}

/** recommendationPageIDs counts only eligible cards and keeps source limits across all pages. */
func recommendationPageIDs(tx *gorm.DB, userID int64, feeds []string, page int) ([]string, bool, error) {
	// The cursor keeps the ranking in PostgreSQL and sends only one candidate batch at a time.
	query, args := recommendationQuery(tx, userID, feeds,
		"SELECT c.id AS article_id,c.identity_key,c.fingerprint,c.url,c.source_id", nil)
	if err := tx.Exec("DECLARE recommendation_candidates NO SCROLL CURSOR FOR "+query, args...).Error; err != nil {
		return nil, false, err
	}

	sources := make(map[string]int)
	identities := make(map[string]bool)
	urls := make(map[string]bool)
	fingerprints := make(map[string]bool)
	ids := make([]string, 0, recommendationLimit)
	currentPage, position := 1, 0
	fetch := fmt.Sprintf("FETCH FORWARD %d FROM recommendation_candidates", recommendationBatchSize)
	for {
		var batch []recommendationCandidate
		if err := tx.Raw(fetch).Scan(&batch).Error; err != nil {
			return nil, false, err
		}
		for _, candidate := range batch {
			normalized, valid := materials.NormalizeURL(candidate.URL)
			if !valid || sources[candidate.SourceID] >= recommendationSourceLimit ||
				identities[candidate.IdentityKey] || urls[normalized] ||
				(candidate.Fingerprint != nil && fingerprints[*candidate.Fingerprint]) {
				continue
			}
			if currentPage > page {
				return ids, true, nil
			}
			if currentPage == page {
				ids = append(ids, candidate.ArticleID)
			}
			sources[candidate.SourceID]++
			identities[candidate.IdentityKey] = true
			urls[normalized] = true
			if candidate.Fingerprint != nil {
				fingerprints[*candidate.Fingerprint] = true
			}
			position++
			if position == recommendationLimit {
				currentPage++
				position = 0
			}
		}
		if len(batch) < recommendationBatchSize {
			return ids, false, nil
		}
	}
}

/** DailyView loads a compact archive and at most one complete issue. */
func (r *Repository) DailyView(ctx context.Context, userID int64, id, date string) (DailyReportView, error) {
	view := DailyReportView{Reports: make([]DailyReportHeading, 0, 30)}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT * FROM (
			SELECT 'scheduled-' || to_char(report_date,'YYYY-MM-DD') AS id,to_char(report_date,'YYYY-MM-DD') AS report_date,title,created_at
			FROM user_daily_reports WHERE user_id=? AND is_scheduled
			UNION ALL
			SELECT issue->>'id',issue->>'date',issue->>'title',(issue->>'createdAt')::timestamptz
			FROM user_daily_reports CROSS JOIN LATERAL jsonb_array_elements(manual_reports) issue WHERE user_id=?
		) issues ORDER BY created_at DESC,id DESC LIMIT 30`, userID, userID).Scan(&view.Reports).Error; err != nil {
			return err
		}

		// Resolve legacy dates separately so links can still open an issue outside the directory.
		if id == "" && date != "" {
			var chosen DailyReportHeading
			query := tx.Raw(`SELECT id FROM (
				SELECT 'scheduled-' || to_char(report_date,'YYYY-MM-DD') AS id,created_at
				FROM user_daily_reports WHERE user_id=? AND report_date=?::text::date AND is_scheduled
				UNION ALL
				SELECT issue->>'id',(issue->>'createdAt')::timestamptz
				FROM user_daily_reports CROSS JOIN LATERAL jsonb_array_elements(manual_reports) issue
				WHERE user_id=? AND issue->>'date'=?
			) issues ORDER BY created_at DESC,id DESC LIMIT 1`, userID, date, userID, date)
			if err := scanOne(query, &chosen); err != nil {
				return err
			}
			id = chosen.ID
		}
		if id == "" {
			if len(view.Reports) == 0 {
				return nil
			}
			id = view.Reports[0].ID
		}
		var issue DailyReport
		if err := readDailyIssue(tx, userID, id, &issue); err != nil {
			return err
		}
		view.Report = &issue
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return view, err
}

/** readDailyIssue retrieves exactly one scheduled or manual report by its public ID. */
func readDailyIssue(tx *gorm.DB, userID int64, id string, issue *DailyReport) error {
	if date, scheduled := strings.CutPrefix(id, "scheduled-"); scheduled {
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return ErrNotFound
		}
		return scanOne(tx.Raw(`SELECT ?::text AS id,to_char(report_date,'YYYY-MM-DD') AS report_date,
			model,title,summary,entries,created_at FROM user_daily_reports
			WHERE user_id=? AND report_date=?::text::date AND is_scheduled`, id, userID, date), issue)
	}
	return scanOne(tx.Raw(`SELECT issue->>'id' AS id,issue->>'date' AS report_date,issue->>'model' AS model,
		issue->>'title' AS title,issue->>'summary' AS summary,issue->'entries' AS entries,
		(issue->>'createdAt')::timestamptz AS created_at
		FROM user_daily_reports CROSS JOIN LATERAL jsonb_array_elements(manual_reports) issue
		WHERE user_id=? AND issue->>'id'=? LIMIT 1`, userID, id), issue)
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

const personalSelect = `SELECT r.article_id,s.id AS source_id,s.icon_url AS source_icon_url,(coalesce(sub.id,0)>0) AS is_subscribed,r.relevance_score::float8 AS score,r.title,r.summary,r.reason,a.url,coalesce(sub.name,s.name) AS source,a.published_at,r.created_at,a.media FROM user_article_results r JOIN articles a ON a.id=r.article_id JOIN sources s ON s.id=a.source_id LEFT JOIN user_source_subscriptions sub ON sub.user_id=r.user_id AND sub.source_id=s.id`
