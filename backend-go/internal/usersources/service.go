package usersources

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/sourcenaming"
	"aihot/backend-go/internal/sourceschedule"
	"aihot/backend-go/internal/tagging"

	"gorm.io/gorm"
)

var ErrNotFound = errors.New("source subscription not found")
var ErrInvalid = errors.New("invalid source subscription")
var ErrAlreadySubscribed = errors.New("source is already subscribed by this user")

type MaterialWriter interface {
	Store(context.Context, materials.Input) (materials.Result, error)
}

type Service struct {
	db             *gorm.DB
	materials      MaterialWriter
	enqueue        func(context.Context, string, string, int64) (int64, error)
	enqueueInitial func(context.Context, string) error
	collectEnabled bool
	schedule       config.SourceSchedule
}

/** NewService creates a new usersources service. */
func NewService(db *gorm.DB, schedule config.SourceSchedule) *Service {
	return &Service{
		db:       db,
		schedule: schedule,
	}
}

/** SetMaterials configures the materials writer for direct article ingestion. */
func (s *Service) SetMaterials(materials MaterialWriter) {
	s.materials = materials
}

/** SetEnqueuer configures the background job enqueuer function. */
func (s *Service) SetEnqueuer(enqueue func(context.Context, string, string, int64) (int64, error)) {
	s.enqueue = enqueue
}

/** SetInitialEnqueuer connects new sources to the regular collection queue. */
func (s *Service) SetInitialEnqueuer(enqueue func(context.Context, string) error) {
	s.enqueueInitial = enqueue
}

/** enqueueNewSource accelerates the first collection, with the normal scan as recovery. */
func (s *Service) enqueueNewSource(ctx context.Context, sourceID string) {
	if sourceID == "" || !s.collectEnabled || s.enqueueInitial == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.enqueueInitial(ctx, sourceID); err != nil {
		log.Printf("initial source collection deferred to scan source_id=%q: %v", sourceID, err)
	}
}

/** SetCollectorEnabled updates collector enabled status flag. */
func (s *Service) SetCollectorEnabled(enabled bool) {
	s.collectEnabled = enabled
}

type Input struct {
	Kind               string `json:"kind,omitempty"`
	Name               string `json:"name"`
	NameInherited      *bool  `json:"nameInherited,omitempty"`
	URL                string `json:"url"`
	ItemSelector       string `json:"itemSelector,omitempty"`
	TitleSelector      string `json:"titleSelector,omitempty"`
	Enabled            *bool  `json:"enabled,omitempty"`
	FilterMode         string `json:"filterMode,omitempty"`
	IntervalMinutes    int    `json:"intervalMinutes"`
	ReuseWindowMinutes int    `json:"reuseWindowMinutes"`
}

type Subscription struct {
	ID                          int64           `json:"id"`
	SourceID                    string          `json:"sourceId"`
	Kind                        string          `json:"kind"`
	Name                        string          `json:"name"`
	NameInherited               bool            `json:"nameInherited"`
	UpdatedExisting             bool            `gorm:"-" json:"updatedExisting,omitempty"`
	URL                         string          `json:"url"`
	ItemSelector                string          `json:"itemSelector,omitempty"`
	TitleSelector               string          `json:"titleSelector,omitempty"`
	Enabled                     bool            `json:"enabled"`
	FilterMode                  string          `json:"filterMode"`
	IntervalMinutes             int             `json:"intervalMinutes"`
	ReuseWindowMinutes          int             `json:"reuseWindowMinutes"`
	EffectiveIntervalMinutes    int             `json:"effectiveIntervalMinutes"`
	EffectiveReuseWindowMinutes int             `json:"effectiveReuseWindowMinutes"`
	LastCheckedAt               *time.Time      `json:"lastCheckedAt"`
	NextFetchAt                 *time.Time      `json:"nextFetchAt"`
	Health                      string          `json:"health"`
	Tags                        model.TextArray `gorm:"column:tags" json:"tags"`
}

type FetchRequest struct {
	JobID  int64  `json:"jobId"`
	Status string `json:"status"`
}

type FetchItem struct {
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	PublishedAt *time.Time `json:"publishedAt"`
}

type FetchResult struct {
	JobID      int64       `json:"jobId"`
	Status     string      `json:"status"`
	CheckedAt  *time.Time  `json:"checkedAt"`
	FoundCount int         `json:"foundCount"`
	NewCount   int         `json:"newCount"`
	Error      string      `json:"error,omitempty"`
	Items      []FetchItem `json:"items"`
}

type IngestInput struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Text    string `json:"text,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
	Author  string `json:"author,omitempty"`
}

type IngestResult struct {
	ArticleID string `json:"articleId"`
	Created   bool   `json:"created"`
	Title     string `json:"title"`
	URL       string `json:"url"`
}

type ExploreItem struct {
	SourceID        string     `gorm:"column:source_id" json:"sourceId"`
	Name            string     `gorm:"column:name" json:"name"`
	Kind            string     `gorm:"column:kind" json:"kind"`
	URL             string     `gorm:"column:url" json:"url"`
	Health          string     `gorm:"column:health" json:"health"`
	SubscriberCount int64      `gorm:"column:subscriber_count" json:"subscriberCount"`
	ArticleCount    int64      `gorm:"column:article_count" json:"articleCount"`
	LastCheckedAt   *time.Time `gorm:"column:last_checked_at" json:"lastCheckedAt"`
	IsSubscribed    bool       `gorm:"column:is_subscribed" json:"isSubscribed"`
	SubscriptionID  int64      `gorm:"column:subscription_id" json:"subscriptionId,omitempty"`
}

type PreviewArticleItem struct {
	ID          string     `gorm:"column:id" json:"id"`
	Title       string     `gorm:"column:title" json:"title"`
	URL         string     `gorm:"column:url" json:"url"`
	Author      string     `gorm:"column:author" json:"author,omitempty"`
	PublishedAt *time.Time `gorm:"column:published_at" json:"publishedAt"`
	Excerpt     string     `gorm:"column:excerpt" json:"excerpt,omitempty"`
}

type SourcePreview struct {
	Source   ExploreItem          `json:"source"`
	Articles []PreviewArticleItem `json:"articles"`
}

type SubscribeExistingInput struct {
	SourceID           string `json:"sourceId"`
	Name               string `json:"name"`
	FilterMode         string `json:"filterMode,omitempty"`
	IntervalMinutes    int    `json:"intervalMinutes"`
	ReuseWindowMinutes int    `json:"reuseWindowMinutes"`
}

const subscriptionSelect = `SELECT sub.id,sub.source_id,src.kind,sub.name,sub.name_inherited,
	coalesce(src.config->>'feedUrl',src.config->>'url','') AS url,
	coalesce(src.config->>'itemSelector','') AS item_selector,
	coalesce(src.config->>'titleSelector','') AS title_selector,
	sub.enabled,sub.filter_mode,sub.interval_minutes,sub.reuse_window_minutes,
	CASE WHEN src.kind IN ('rss','web_list') THEN src.interval_minutes
	     ELSE greatest(src.interval_minutes,src.reuse_window_minutes) END AS effective_interval_minutes,
	CASE WHEN src.kind IN ('rss','web_list') THEN least(src.reuse_window_minutes,nullif(?,0))
	     ELSE src.reuse_window_minutes END AS effective_reuse_window_minutes,
	src.last_ok_at AS last_checked_at,src.next_fetch_at,src.health,
	ARRAY(SELECT name FROM (` + tagging.SourceLabelRanking + `) ranked ORDER BY article_count DESC,name,id) AS tags
	FROM user_source_subscriptions sub JOIN sources src ON src.id=sub.source_id`

/** List retrieves all source subscriptions for a given user. */
func (s *Service) List(ctx context.Context, userID int64) ([]Subscription, error) {
	var rows []Subscription
	err := s.db.WithContext(ctx).Raw(subscriptionSelect+` WHERE sub.user_id=? ORDER BY sub.created_at DESC,sub.id`, s.schedule.AutoReuseWindow, userID).Scan(&rows).Error
	if rows == nil {
		rows = []Subscription{}
	}

	return rows, err
}

/** Get retrieves a single source subscription by ID for a given user. */
func (s *Service) Get(ctx context.Context, userID, id int64) (Subscription, error) {
	var row Subscription
	err := s.db.WithContext(ctx).Raw(subscriptionSelect+` WHERE sub.user_id=? AND sub.id=?`, s.schedule.AutoReuseWindow, userID, id).Scan(&row).Error
	if err != nil {
		return Subscription{}, err
	}

	var count int64
	if err := s.db.WithContext(ctx).Raw(`SELECT count(*) FROM user_source_subscriptions WHERE user_id=? AND id=?`, userID, id).Row().Scan(&count); err != nil {
		return Subscription{}, err
	}

	if count == 0 {
		return Subscription{}, ErrNotFound
	}

	return row, nil
}

/** Explore searches or recommends public sources for users to discover and follow. */
func (s *Service) Explore(ctx context.Context, userID int64, query string, limit, offset int) ([]ExploreItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	if offset < 0 {
		offset = 0
	}

	query = strings.TrimSpace(query)

	var sqlQuery string
	var args []any
	if query == "" {
		// 检索高热度与最新活跃的公共信源（排除失效与单篇投递源）
		sqlQuery = `SELECT 
			s.id AS source_id,
			s.name,
			s.kind,
			coalesce(s.config->>'feedUrl', s.config->>'url', '') AS url,
			s.health,
			s.last_ok_at AS last_checked_at,
			coalesce(sub_count.count, 0) AS subscriber_count,
			coalesce(art_count.count, 0) AS article_count,
			(coalesce(my_sub.id, 0) > 0) AS is_subscribed,
			coalesce(my_sub.id, 0) AS subscription_id
		FROM sources s
		LEFT JOIN (
			SELECT source_id, COUNT(*) AS count 
			FROM user_source_subscriptions 
			WHERE enabled = true 
			GROUP BY source_id
		) sub_count ON sub_count.source_id = s.id
		LEFT JOIN (
			SELECT source_id, COUNT(*) AS count 
			FROM articles 
			GROUP BY source_id
		) art_count ON art_count.source_id = s.id
		LEFT JOIN user_source_subscriptions my_sub ON my_sub.source_id = s.id AND my_sub.user_id = ?
		WHERE s.kind IN ('rss', 'web_list')
		  AND s.health != 'failing'
		ORDER BY is_subscribed ASC, subscriber_count DESC, s.last_ok_at DESC NULLS LAST, s.id ASC
		LIMIT ? OFFSET ?`
		args = []any{
			userID,
			limit,
			offset,
		}
	} else {
		// 模糊搜索：支持匹配信源名称与链接地址
		pattern := "%" + query + "%"
		sqlQuery = `SELECT 
			s.id AS source_id,
			s.name,
			s.kind,
			coalesce(s.config->>'feedUrl', s.config->>'url', '') AS url,
			s.health,
			s.last_ok_at AS last_checked_at,
			coalesce(sub_count.count, 0) AS subscriber_count,
			coalesce(art_count.count, 0) AS article_count,
			(coalesce(my_sub.id, 0) > 0) AS is_subscribed,
			coalesce(my_sub.id, 0) AS subscription_id
		FROM sources s
		LEFT JOIN (
			SELECT source_id, COUNT(*) AS count 
			FROM user_source_subscriptions 
			WHERE enabled = true 
			GROUP BY source_id
		) sub_count ON sub_count.source_id = s.id
		LEFT JOIN (
			SELECT source_id, COUNT(*) AS count 
			FROM articles 
			GROUP BY source_id
		) art_count ON art_count.source_id = s.id
		LEFT JOIN user_source_subscriptions my_sub ON my_sub.source_id = s.id AND my_sub.user_id = ?
		WHERE s.kind IN ('rss', 'web_list')
		  AND s.health != 'failing'
		  AND (s.name ILIKE ? OR coalesce(s.config->>'feedUrl', s.config->>'url', '') ILIKE ?)
		ORDER BY is_subscribed ASC, subscriber_count DESC, s.last_ok_at DESC NULLS LAST, s.id ASC
		LIMIT ? OFFSET ?`
		args = []any{
			userID,
			pattern,
			pattern,
			limit,
			offset,
		}
	}

	var rows []ExploreItem
	err := s.db.WithContext(ctx).Raw(sqlQuery, args...).Scan(&rows).Error
	if rows == nil {
		rows = []ExploreItem{}
	}
	for i := range rows {
		rows[i].Name = sourcenaming.LegacyDisplayName(rows[i].Kind, rows[i].Name, rows[i].SourceID, rows[i].URL)
	}

	return rows, err
}

/** Preview retrieves public metadata and recent articles for a given source. */
func (s *Service) Preview(ctx context.Context, userID int64, sourceID string) (SourcePreview, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return SourcePreview{}, ErrNotFound
	}

	// 读取公共信源基本信息与订阅状态
	var item ExploreItem
	err := s.db.WithContext(ctx).Raw(`SELECT 
		s.id AS source_id,
		s.name,
		s.kind,
		coalesce(s.config->>'feedUrl', s.config->>'url', '') AS url,
		s.health,
		s.last_ok_at AS last_checked_at,
		coalesce(sub_count.count, 0) AS subscriber_count,
		coalesce(art_count.count, 0) AS article_count,
		(coalesce(my_sub.id, 0) > 0) AS is_subscribed,
		coalesce(my_sub.id, 0) AS subscription_id
	FROM sources s
	LEFT JOIN (
		SELECT source_id, COUNT(*) AS count 
		FROM user_source_subscriptions 
		WHERE enabled = true 
		GROUP BY source_id
	) sub_count ON sub_count.source_id = s.id
	LEFT JOIN (
		SELECT source_id, COUNT(*) AS count 
		FROM articles 
		GROUP BY source_id
	) art_count ON art_count.source_id = s.id
	LEFT JOIN user_source_subscriptions my_sub ON my_sub.source_id = s.id AND my_sub.user_id = ?
	WHERE s.id = ? AND s.kind IN ('rss', 'web_list')`, userID, sourceID).Scan(&item).Error
	if err != nil {
		return SourcePreview{}, err
	}

	if item.SourceID == "" {
		return SourcePreview{}, ErrNotFound
	}
	item.Name = sourcenaming.LegacyDisplayName(item.Kind, item.Name, item.SourceID, item.URL)

	// 读取该信源最近收录的 10 篇文章公开导读
	var articles []PreviewArticleItem
	err = s.db.WithContext(ctx).Raw(`SELECT 
		id,
		title,
		url,
		author,
		published_at,
		excerpt
	FROM articles
	WHERE source_id = ?
	ORDER BY coalesce(published_at, discovered_at) DESC
	LIMIT 10`, sourceID).Scan(&articles).Error
	if err != nil {
		return SourcePreview{}, err
	}

	if articles == nil {
		articles = []PreviewArticleItem{}
	}

	return SourcePreview{
		Source:   item,
		Articles: articles,
	}, nil
}

/** SubscribeExisting allows a user to follow an existing public source with custom settings. */
func (s *Service) SubscribeExisting(ctx context.Context, userID int64, input SubscribeExistingInput) (Subscription, error) {
	sourceID := strings.TrimSpace(input.SourceID)
	if sourceID == "" {
		return Subscription{}, ErrInvalid
	}

	if input.IntervalMinutes <= 0 {
		input.IntervalMinutes = 60
	}

	if input.ReuseWindowMinutes <= 0 {
		input.ReuseWindowMinutes = 10
	}

	var id int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize default-name reads with the worker publishing a new shared name.
		defaultName, err := sharedDisplayName(tx, sourceID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}

		if err != nil {
			return err
		}

		name := strings.TrimSpace(input.Name)
		if utf8.RuneCountInString(name) > 200 || input.IntervalMinutes > 1440 || input.ReuseWindowMinutes > 1440 {
			return ErrInvalid
		}
		inherited := name == ""
		if inherited {
			name = defaultName
		}

		filterMode := strings.TrimSpace(input.FilterMode)
		if filterMode != "all" {
			filterMode = "label"
		}

		// 插入或更新用户订阅
		if err := tx.Raw(`INSERT INTO user_source_subscriptions AS sub(user_id, source_id, name, name_inherited, filter_mode, interval_minutes, reuse_window_minutes, cold_start_pending)
			VALUES(?, ?, ?, ?, ?, ?, ?, false)
			ON CONFLICT(user_id, source_id)
			DO UPDATE SET name = CASE WHEN excluded.name_inherited AND NOT sub.name_inherited THEN sub.name ELSE excluded.name END,
			name_inherited = sub.name_inherited AND excluded.name_inherited, filter_mode = excluded.filter_mode,
			enabled = true, interval_minutes = excluded.interval_minutes, reuse_window_minutes = excluded.reuse_window_minutes,
			activated_at = CASE WHEN sub.enabled THEN sub.activated_at ELSE now() END, updated_at = now()
			RETURNING id`, userID, sourceID, name, inherited, filterMode, input.IntervalMinutes, input.ReuseWindowMinutes).Row().Scan(&id); err != nil {
			return err
		}

		// 重新计算信源聚合调度周期
		return sourceschedule.Recompute(tx, sourceID, s.schedule)
	})
	if err != nil {
		return Subscription{}, err
	}

	return s.Get(ctx, userID, id)
}

/** Create registers a new user source subscription. */
func (s *Service) Create(ctx context.Context, userID int64, input Input) (Subscription, error) {
	input = withDefaults(input)
	input.NameInherited = nil
	if err := validate(input); err != nil {
		return Subscription{}, err
	}

	var id int64
	var newSource string
	var updatedExisting bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sourceID, created, err := resolveSource(tx, input, userID)
		if err != nil {
			return err
		}
		if created {
			newSource = sourceID
		}
		if err := sourceschedule.Lock(tx, sourceID); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM user_source_subscriptions WHERE user_id=? AND source_id=?)`, userID, sourceID).Row().Scan(&updatedExisting); err != nil {
			return err
		}

		filterMode := strings.TrimSpace(input.FilterMode)
		if filterMode != "all" {
			filterMode = "label"
		}

		if err := tx.Raw(`INSERT INTO user_source_subscriptions AS sub(user_id,source_id,name,filter_mode,interval_minutes,reuse_window_minutes,cold_start_pending)
			VALUES(?,?,?,?,?,?,true) ON CONFLICT(user_id,source_id) DO UPDATE SET name=excluded.name,name_inherited=false,
			filter_mode=excluded.filter_mode,enabled=true,interval_minutes=excluded.interval_minutes,reuse_window_minutes=excluded.reuse_window_minutes,
			activated_at=CASE WHEN sub.enabled THEN sub.activated_at ELSE now() END,updated_at=now() RETURNING id`,
			userID, sourceID, strings.TrimSpace(input.Name), filterMode, input.IntervalMinutes, input.ReuseWindowMinutes).Row().Scan(&id); err != nil {
			return err
		}

		return sourceschedule.Recompute(tx, sourceID, s.schedule)
	})
	if err != nil {
		return Subscription{}, err
	}

	s.enqueueNewSource(ctx, newSource)
	result, err := s.Get(ctx, userID, id)
	result.UpdatedExisting = updatedExisting
	return result, err
}

/** withDefaults fills in reasonable default intervals if not specified. */
func withDefaults(input Input) Input {
	if input.Kind == "" {
		input.Kind = "rss"
	}

	if input.IntervalMinutes == 0 {
		input.IntervalMinutes = 60
	}

	if input.ReuseWindowMinutes == 0 {
		input.ReuseWindowMinutes = 10
	}

	if input.FilterMode != "all" {
		input.FilterMode = "label"
	}

	return input
}

/** Update modifies an existing source subscription. */
func (s *Service) Update(ctx context.Context, userID, id int64, input Input) (Subscription, error) {
	if err := validate(input); err != nil {
		return Subscription{}, err
	}

	var createdSource string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var oldSource string
		if err := tx.Raw(`SELECT source_id FROM user_source_subscriptions WHERE user_id=? AND id=?`, userID, id).Row().Scan(&oldSource); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}

		newSource, created, err := resolveSource(tx, input, userID)
		if err != nil {
			return err
		}
		if created {
			createdSource = newSource
		}

		// Lock both plans in the same order when concurrent edits move between them.
		if err := sourceschedule.Lock(tx, min(oldSource, newSource)); err != nil {
			return err
		}
		if oldSource != newSource {
			if err := sourceschedule.Lock(tx, max(oldSource, newSource)); err != nil {
				return err
			}
		}

		var previousName string
		var previousInherited bool
		if err := tx.Raw(`SELECT name,name_inherited FROM user_source_subscriptions WHERE user_id=? AND id=? AND source_id=? FOR UPDATE`, userID, id, oldSource).Row().Scan(&previousName, &previousInherited); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}

		var duplicate int64
		if err := tx.Table("user_source_subscriptions").Where("user_id=? AND source_id=? AND id<>?", userID, newSource, id).Count(&duplicate).Error; err != nil {
			return err
		}

		if duplicate > 0 {
			return ErrAlreadySubscribed
		}

		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}

		filterMode := strings.TrimSpace(input.FilterMode)
		if filterMode != "all" {
			filterMode = "label"
		}

		name := strings.TrimSpace(input.Name)
		inherited := previousInherited && name == previousName
		if input.NameInherited != nil {
			inherited = *input.NameInherited
		}
		if inherited {
			name, err = sharedDisplayName(tx, newSource)
			if err != nil {
				return err
			}
		}

		if err := tx.Exec(`UPDATE user_source_subscriptions SET source_id=?,name=?,name_inherited=?,filter_mode=?,enabled=?,interval_minutes=?,reuse_window_minutes=?,cold_start_pending=CASE WHEN source_id<>? THEN true ELSE cold_start_pending END,activated_at=CASE WHEN enabled<>? OR source_id<>? THEN now() ELSE activated_at END,updated_at=now() WHERE user_id=? AND id=?`, newSource, name, inherited, filterMode, enabled, input.IntervalMinutes, input.ReuseWindowMinutes, newSource, enabled, newSource, userID, id).Error; err != nil {
			return err
		}

		if oldSource == newSource {
			return sourceschedule.Recompute(tx, newSource, s.schedule)
		}

		if err := sourceschedule.Recompute(tx, oldSource, s.schedule); err != nil {
			return err
		}

		return sourceschedule.Recompute(tx, newSource, s.schedule)
	})
	if err != nil {
		return Subscription{}, err
	}

	s.enqueueNewSource(ctx, createdSource)
	return s.Get(ctx, userID, id)
}

/** SetEnabled pauses or resumes a source subscription. */
func (s *Service) SetEnabled(ctx context.Context, userID, id int64, enabled bool) (Subscription, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sourceID, err := lockSubscriptionSource(tx, userID, id)
		if err != nil {
			return err
		}
		result := tx.Exec(`UPDATE user_source_subscriptions SET enabled=?,activated_at=CASE WHEN ? AND NOT enabled THEN now() ELSE activated_at END,updated_at=now() WHERE user_id=? AND id=? AND source_id=?`, enabled, enabled, userID, id, sourceID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}

		return sourceschedule.Recompute(tx, sourceID, s.schedule)
	})
	if err != nil {
		return Subscription{}, err
	}

	return s.Get(ctx, userID, id)
}

/** lockSubscriptionSource orders source locks before subscription changes. */
func lockSubscriptionSource(tx *gorm.DB, userID, id int64) (string, error) {
	var sourceID string
	if err := tx.Raw(`SELECT source_id FROM user_source_subscriptions WHERE user_id=? AND id=?`, userID, id).Row().Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	return sourceID, sourceschedule.Lock(tx, sourceID)
}

/** Delete removes a user source subscription while keeping historical data intact. */
func (s *Service) Delete(ctx context.Context, userID, id int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sourceID, err := lockSubscriptionSource(tx, userID, id)
		if err != nil {
			return err
		}
		result := tx.Exec(`DELETE FROM user_source_subscriptions WHERE user_id=? AND id=? AND source_id=?`, userID, id, sourceID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}

		return sourceschedule.Recompute(tx, sourceID, s.schedule)
	})
}

/** EnqueueFetch enqueues a manual fetch for a subscribed source. */
func (s *Service) EnqueueFetch(ctx context.Context, userID, id int64) (FetchRequest, error) {
	if s.enqueue == nil {
		return FetchRequest{}, errors.New("source fetch queue unavailable")
	}

	var row struct {
		SourceID string `gorm:"column:source_id"`
		Kind     string `gorm:"column:kind"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT sub.source_id, src.kind FROM user_source_subscriptions sub JOIN sources src ON src.id=sub.source_id WHERE sub.user_id=? AND sub.id=?`, userID, id).Scan(&row).Error; errors.Is(err, sql.ErrNoRows) || row.SourceID == "" {
		return FetchRequest{}, ErrNotFound
	} else if err != nil {
		return FetchRequest{}, err
	}

	if !s.collectEnabled {
		return FetchRequest{Status: "disabled"}, errors.New("采集功能当前已关闭")
	}

	jobID, err := s.enqueue(ctx, row.SourceID, row.Kind, userID)
	if err != nil {
		return FetchRequest{}, err
	}

	return FetchRequest{
		JobID:  jobID,
		Status: "queued",
	}, nil
}

/** FetchStatus queries the current execution state of a manual fetch job. */
func (s *Service) FetchStatus(ctx context.Context, userID, id, jobID int64) (FetchResult, error) {
	var sourceID string
	if err := s.db.WithContext(ctx).Raw(`SELECT source_id FROM user_source_subscriptions WHERE user_id=? AND id=?`, userID, id).Row().Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
		return FetchResult{}, ErrNotFound
	} else if err != nil {
		return FetchResult{}, err
	}

	var out FetchResult
	out.JobID = jobID

	var state string
	if err := s.db.WithContext(ctx).Raw(`SELECT state FROM river_job WHERE id=? AND kind='myhot_collect_source' AND args->>'sourceId'=?`, jobID, sourceID).Row().Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return FetchResult{}, ErrNotFound
	} else if err != nil {
		return FetchResult{}, err
	}

	switch state {
	case "available", "pending", "scheduled", "retryable":
		out.Status = "queued"
	case "running":
		out.Status = "running"
	case "completed":
		out.Status = "succeeded"
	case "discarded", "cancelled":
		out.Status = "failed"
	default:
		out.Status = state
	}

	var result struct {
		StartedAt  *time.Time `gorm:"column:started_at"`
		FinishedAt *time.Time `gorm:"column:finished_at"`
		Status     string     `gorm:"column:status"`
		Found      int        `gorm:"column:found_count"`
		New        int        `gorm:"column:new_count"`
		Error      *string    `gorm:"column:error"`
		Detail     []byte     `gorm:"column:detail"`
	}
	_ = s.db.WithContext(ctx).Raw(`SELECT started_at,finished_at,status,found_count,new_count,error,detail FROM fetch_runs WHERE source_id=? AND started_at >= (SELECT created_at FROM river_job WHERE id=?) ORDER BY id DESC LIMIT 1`, sourceID, jobID).Scan(&result).Error

	out.CheckedAt, out.FoundCount, out.NewCount = result.FinishedAt, result.Found, result.New
	if result.Error != nil {
		out.Error = *result.Error
	}

	var detail map[string]any
	if json.Unmarshal(result.Detail, &detail) == nil && detail["state"] == "reused" {
		out.Status = "reused"
	}

	if state == "completed" && result.Status == "failed" {
		out.Status = "failed"
	}

	if state == "completed" && result.Status == "skipped" {
		out.Status = "reused"
	}

	if out.Status == "succeeded" || out.Status == "reused" {
		var items []FetchItem
		err := s.db.WithContext(ctx).Raw(`SELECT title,url,published_at FROM articles WHERE source_id=? ORDER BY discovered_at DESC,id DESC LIMIT 20`, sourceID).Scan(&items).Error
		if err != nil {
			return FetchResult{}, err
		}

		if items == nil {
			items = []FetchItem{}
		}

		out.Items = items
	}

	return out, nil
}

/** IngestDirect stores a single article directly into the user's isolated external source. */
func (s *Service) IngestDirect(ctx context.Context, userID int64, input IngestInput) (IngestResult, error) {
	if s.materials == nil {
		return IngestResult{}, errors.New("materials service unavailable")
	}

	rawURL := strings.TrimSpace(input.URL)
	if rawURL == "" {
		return IngestResult{}, errors.New("文章链接不能为空")
	}

	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return IngestResult{}, errors.New("无效的网页地址")
	}

	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = u.Hostname() + u.Path
	}

	sourceID := fmt.Sprintf("ingest-user-%d", userID)

	// Ensure synthetic source and user subscription exist
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO sources(id,name,kind,config,participation_mode,interval_minutes,reuse_window_minutes,site_fulltext,syndicate_fulltext,enabled,health,next_fetch_at) VALUES(?,'个人手动投递','external','{}'::jsonb,'isolated',1440,60,true,false,true,'ok',NULL) ON CONFLICT (id) DO NOTHING`, sourceID).Error; err != nil {
			return err
		}

		return tx.Exec(`INSERT INTO user_source_subscriptions(user_id,source_id,name,interval_minutes,reuse_window_minutes,enabled) VALUES(?,?,'个人手动投递',1440,60,true) ON CONFLICT (user_id,source_id) DO NOTHING`, userID, sourceID).Error
	})
	if err != nil {
		return IngestResult{}, err
	}

	bodyStatus := "pending"
	if strings.TrimSpace(input.Text) != "" {
		bodyStatus = "ok"
	}

	now := time.Now().UTC()
	identity := "url:" + rawURL

	// Persist material into articles repository
	stored, err := s.materials.Store(ctx, materials.Input{
		SourceID:     sourceID,
		URL:          rawURL,
		IdentityKey:  identity,
		Title:        title,
		Author:       strings.TrimSpace(input.Author),
		Excerpt:      strings.TrimSpace(input.Excerpt),
		BodyText:     strings.TrimSpace(input.Text),
		BodyStatus:   bodyStatus,
		DiscoveredAt: now,
		PublishedAt:  &now,
		Via:          "ingest",
	})
	if err != nil {
		return IngestResult{}, err
	}

	return IngestResult{
		ArticleID: stored.ArticleID,
		Created:   stored.Created,
		Title:     title,
		URL:       rawURL,
	}, nil
}

/** validate checks input parameters for validity. */
func validate(in Input) error {
	name := strings.TrimSpace(in.Name)
	if (name == "" && (in.NameInherited == nil || !*in.NameInherited)) || utf8.RuneCountInString(name) > 200 || in.IntervalMinutes < 1 || in.IntervalMinutes > 1440 || in.ReuseWindowMinutes < 1 || in.ReuseWindowMinutes > 1440 || len(in.URL) > 2048 {
		return ErrInvalid
	}

	if in.Kind != "" && in.Kind != "rss" && in.Kind != "web_list" {
		return ErrInvalid
	}

	if in.FilterMode != "" && in.FilterMode != "label" && in.FilterMode != "all" {
		return ErrInvalid
	}

	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return ErrInvalid
	}

	return nil
}

/** resolveSource resolves or creates a source based on input kind (rss or web_list). */
func resolveSource(tx *gorm.DB, input Input, userID int64) (string, bool, error) {
	kind := input.Kind
	if kind == "" {
		kind = "rss"
	}

	if kind == "web_list" {
		return resolveWebListSource(tx, input, userID)
	}

	return resolveRSSSource(tx, input.URL, userID)
}

/** resolveWebListSource resolves or inserts a web_list source. */
func resolveWebListSource(tx *gorm.DB, input Input, userID int64) (string, bool, error) {
	rawURL := strings.TrimSpace(input.URL)
	itemSelector := strings.TrimSpace(input.ItemSelector)
	titleSelector := strings.TrimSpace(input.TitleSelector)

	identity := identityFor("web_list:" + rawURL + ":" + itemSelector + ":" + titleSelector)

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('myhot:source-identity'),hashtext(?))`, identity).Error; err != nil {
		return "", false, err
	}

	var id string
	err := tx.Raw(`SELECT id FROM sources WHERE kind='web_list' AND collector_identity=?`, identity).Row().Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}

	id = "web-" + identity[:32]
	configMap := map[string]string{
		"url":           rawURL,
		"itemSelector":  itemSelector,
		"titleSelector": titleSelector,
		"parseMode":     "html",
	}
	configBytes, _ := json.Marshal(configMap)

	return insertNamedSource(tx, id, "web_list", rawURL, identity, configBytes, userID)
}

/** resolveRSSSource resolves or inserts a standard RSS/Atom source. */
func resolveRSSSource(tx *gorm.DB, raw string, userID int64) (string, bool, error) {
	identity := identityFor(raw)
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('myhot:rss-identity'),hashtext(?))`, identity).Error; err != nil {
		return "", false, err
	}

	var id string
	err := tx.Raw(`SELECT id FROM sources WHERE kind='rss' AND collector_identity=?`, identity).Row().Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}

	var legacy []struct {
		ID     string `gorm:"column:id"`
		Config []byte `gorm:"column:config"`
	}
	if err := tx.Raw(`SELECT id,config FROM sources WHERE kind='rss' AND collector_identity IS NULL ORDER BY id`).Scan(&legacy).Error; err != nil {
		return "", false, err
	}

	for _, row := range legacy {
		var config map[string]any
		if err := json.Unmarshal(row.Config, &config); err != nil {
			continue
		}

		address, _ := config["feedUrl"].(string)
		if address == "" {
			address, _ = config["url"].(string)
		}

		if address != "" && identityFor(address) == identity {
			id = row.ID
			break
		}
	}

	if id != "" {
		if err := tx.Exec(`UPDATE sources SET collector_identity=?,participation_mode='isolated' WHERE id=?`, identity, id).Error; err != nil {
			return "", false, err
		}

		return id, false, nil
	}

	id = "rss-" + identity[:32]
	config, _ := json.Marshal(map[string]string{
		"feedUrl": strings.TrimSpace(raw),
	})

	return insertNamedSource(tx, id, "rss", raw, identity, config, userID)
}

/** insertNamedSource creates a shared source and records who requested its first fetch. */
func insertNamedSource(tx *gorm.DB, id, kind, address, identity string, config []byte, userID int64) (string, bool, error) {
	name, err := sourcenaming.ReserveName(tx, id, sourcenaming.Provisional(id, address), "")
	if err != nil {
		return "", false, err
	}
	if err := tx.Exec(`INSERT INTO sources(id,name,kind,config,participation_mode,interval_minutes,reuse_window_minutes,site_fulltext,syndicate_fulltext,enabled,health,next_fetch_at,collector_identity)
		VALUES(?,?,?,?::jsonb,'isolated',60,10,false,false,false,'paused',now(),?)`, id, name, kind, string(config), identity).Error; err != nil {
		return "", false, err
	}
	if err := sourcenaming.Register(tx, id, userID); err != nil {
		return "", false, err
	}
	return id, true, nil
}

/** sharedDisplayName reads the shared name while preventing concurrent inheritance updates. */
func sharedDisplayName(tx *gorm.DB, id string) (string, error) {
	var kind, name, address string
	err := tx.Raw(`SELECT kind,name,coalesce(config->>'feedUrl',config->>'url','')
		FROM sources WHERE id=? AND kind IN ('rss','web_list') FOR NO KEY UPDATE`, id).Row().Scan(&kind, &name, &address)
	return sourcenaming.LegacyDisplayName(kind, name, id, address), err
}

/** identityFor generates a deterministic SHA-256 identity string for a URL or key. */
func identityFor(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		sum := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(sum[:])
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""

	if u.Scheme == "http" && strings.HasSuffix(u.Host, ":80") {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	}

	if u.Scheme == "https" && strings.HasSuffix(u.Host, ":443") {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}

	query := u.Query()
	u.RawQuery = query.Encode()

	sum := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(sum[:])
}

func ValidateInput(in Input) error { return validate(in) }
func ParseID(raw string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(raw, &id)
	if err != nil || id < 1 {
		return 0, ErrNotFound
	}
	return id, nil
}
