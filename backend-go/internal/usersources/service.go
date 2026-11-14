package usersources

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"aihot/backend-go/internal/materials"
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
	enqueue        func(context.Context, string, string) (int64, error)
	collectEnabled bool
}

/** NewService creates a new usersources service. */
func NewService(db *gorm.DB) *Service {
	return &Service{
		db: db,
	}
}

/** SetMaterials configures the materials writer for direct article ingestion. */
func (s *Service) SetMaterials(materials MaterialWriter) {
	s.materials = materials
}

/** SetEnqueuer configures the background job enqueuer function. */
func (s *Service) SetEnqueuer(enqueue func(context.Context, string, string) (int64, error)) {
	s.enqueue = enqueue
}

/** SetCollectorEnabled updates collector enabled status flag. */
func (s *Service) SetCollectorEnabled(enabled bool) {
	s.collectEnabled = enabled
}

type Input struct {
	Kind               string `json:"kind,omitempty"`
	Name               string `json:"name"`
	URL                string `json:"url"`
	ItemSelector       string `json:"itemSelector,omitempty"`
	TitleSelector      string `json:"titleSelector,omitempty"`
	Enabled            *bool  `json:"enabled,omitempty"`
	IntervalMinutes    int    `json:"intervalMinutes"`
	ReuseWindowMinutes int    `json:"reuseWindowMinutes"`
}

type Subscription struct {
	ID                          int64      `json:"id"`
	SourceID                    string     `json:"sourceId"`
	Kind                        string     `json:"kind"`
	Name                        string     `json:"name"`
	URL                         string     `json:"url"`
	ItemSelector                string     `json:"itemSelector,omitempty"`
	TitleSelector               string     `json:"titleSelector,omitempty"`
	Enabled                     bool       `json:"enabled"`
	IntervalMinutes             int        `json:"intervalMinutes"`
	ReuseWindowMinutes          int        `json:"reuseWindowMinutes"`
	EffectiveIntervalMinutes    int        `json:"effectiveIntervalMinutes"`
	EffectiveReuseWindowMinutes int        `json:"effectiveReuseWindowMinutes"`
	LastCheckedAt               *time.Time `json:"lastCheckedAt"`
	NextFetchAt                 *time.Time `json:"nextFetchAt"`
	Health                      string     `json:"health"`
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

const subscriptionSelect = `SELECT sub.id,sub.source_id,src.kind,sub.name,
	coalesce(src.config->>'feedUrl',src.config->>'url','') AS url,
	coalesce(src.config->>'itemSelector','') AS item_selector,
	coalesce(src.config->>'titleSelector','') AS title_selector,
	sub.enabled,sub.interval_minutes,sub.reuse_window_minutes,
	greatest(src.interval_minutes,src.reuse_window_minutes) AS effective_interval_minutes,src.reuse_window_minutes AS effective_reuse_window_minutes,
	src.last_ok_at AS last_checked_at,src.next_fetch_at,src.health
	FROM user_source_subscriptions sub JOIN sources src ON src.id=sub.source_id`

/** List retrieves all source subscriptions for a given user. */
func (s *Service) List(ctx context.Context, userID int64) ([]Subscription, error) {
	var rows []Subscription
	err := s.db.WithContext(ctx).Raw(subscriptionSelect+` WHERE sub.user_id=? ORDER BY sub.created_at DESC,sub.id`, userID).Scan(&rows).Error
	if rows == nil {
		rows = []Subscription{}
	}

	return rows, err
}

/** Get retrieves a single source subscription by ID for a given user. */
func (s *Service) Get(ctx context.Context, userID, id int64) (Subscription, error) {
	var row Subscription
	err := s.db.WithContext(ctx).Raw(subscriptionSelect+` WHERE sub.user_id=? AND sub.id=?`, userID, id).Scan(&row).Error
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

/** Create registers a new user source subscription. */
func (s *Service) Create(ctx context.Context, userID int64, input Input) (Subscription, error) {
	input = withDefaults(input)
	if err := validate(input); err != nil {
		return Subscription{}, err
	}

	var id int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sourceID, err := resolveSource(tx, input)
		if err != nil {
			return err
		}

		if err := tx.Raw(`INSERT INTO user_source_subscriptions(user_id,source_id,name,interval_minutes,reuse_window_minutes,cold_start_pending) VALUES(?,?,?,?,?,true) ON CONFLICT(user_id,source_id) DO UPDATE SET name=excluded.name,enabled=true,interval_minutes=excluded.interval_minutes,reuse_window_minutes=excluded.reuse_window_minutes,cold_start_pending=true,activated_at=now(),updated_at=now() RETURNING id`, userID, sourceID, strings.TrimSpace(input.Name), input.IntervalMinutes, input.ReuseWindowMinutes).Row().Scan(&id); err != nil {
			return err
		}

		return recompute(tx, sourceID, true)
	})
	if err != nil {
		return Subscription{}, err
	}

	return s.Get(ctx, userID, id)
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

	return input
}

/** Update modifies an existing source subscription. */
func (s *Service) Update(ctx context.Context, userID, id int64, input Input) (Subscription, error) {
	if err := validate(input); err != nil {
		return Subscription{}, err
	}

	var oldSource string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT source_id FROM user_source_subscriptions WHERE user_id=? AND id=? FOR UPDATE`, userID, id).Row().Scan(&oldSource); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}

		newSource, err := resolveSource(tx, input)
		if err != nil {
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

		if err := tx.Exec(`UPDATE user_source_subscriptions SET source_id=?,name=?,enabled=?,interval_minutes=?,reuse_window_minutes=?,cold_start_pending=CASE WHEN source_id<>? THEN true ELSE cold_start_pending END,activated_at=CASE WHEN enabled<>? OR source_id<>? THEN now() ELSE activated_at END,updated_at=now() WHERE user_id=? AND id=?`, newSource, strings.TrimSpace(input.Name), enabled, input.IntervalMinutes, input.ReuseWindowMinutes, newSource, enabled, newSource, userID, id).Error; err != nil {
			return err
		}

		if oldSource == newSource {
			return recompute(tx, newSource, true)
		}

		if err := recompute(tx, oldSource, true); err != nil {
			return err
		}

		return recompute(tx, newSource, true)
	})
	if err != nil {
		return Subscription{}, err
	}

	return s.Get(ctx, userID, id)
}

/** SetEnabled pauses or resumes a source subscription. */
func (s *Service) SetEnabled(ctx context.Context, userID, id int64, enabled bool) (Subscription, error) {
	var sourceID string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`UPDATE user_source_subscriptions SET enabled=?,activated_at=CASE WHEN ? THEN now() ELSE activated_at END,updated_at=now() WHERE user_id=? AND id=? RETURNING source_id`, enabled, enabled, userID, id).Row().Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}

		return recompute(tx, sourceID, enabled)
	})
	if err != nil {
		return Subscription{}, err
	}

	return s.Get(ctx, userID, id)
}

/** Delete removes a user source subscription while keeping historical data intact. */
func (s *Service) Delete(ctx context.Context, userID, id int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sourceID string
		if err := tx.Raw(`DELETE FROM user_source_subscriptions WHERE user_id=? AND id=? RETURNING source_id`, userID, id).Row().Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}

		return recompute(tx, sourceID, false)
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

	jobID, err := s.enqueue(ctx, row.SourceID, row.Kind)
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
	if name == "" || utf8.RuneCountInString(name) > 200 || in.IntervalMinutes < 1 || in.IntervalMinutes > 1440 || in.ReuseWindowMinutes < 1 || in.ReuseWindowMinutes > 1440 || len(in.URL) > 2048 {
		return ErrInvalid
	}

	if in.Kind != "" && in.Kind != "rss" && in.Kind != "web_list" {
		return ErrInvalid
	}

	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return ErrInvalid
	}

	return nil
}

/** resolveSource resolves or creates a source based on input kind (rss or web_list). */
func resolveSource(tx *gorm.DB, input Input) (string, error) {
	kind := input.Kind
	if kind == "" {
		kind = "rss"
	}

	if kind == "web_list" {
		return resolveWebListSource(tx, input)
	}

	return resolveRSSSource(tx, input.URL)
}

/** resolveWebListSource resolves or inserts a web_list source. */
func resolveWebListSource(tx *gorm.DB, input Input) (string, error) {
	rawURL := strings.TrimSpace(input.URL)
	itemSelector := strings.TrimSpace(input.ItemSelector)
	titleSelector := strings.TrimSpace(input.TitleSelector)

	identity := identityFor("web_list:" + rawURL + ":" + itemSelector + ":" + titleSelector)

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('myhot:source-identity'),hashtext(?))`, identity).Error; err != nil {
		return "", err
	}

	var id string
	err := tx.Raw(`SELECT id FROM sources WHERE kind='web_list' AND collector_identity=? FOR UPDATE`, identity).Row().Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	id = "web-" + identity[:32]
	configMap := map[string]string{
		"url":           rawURL,
		"itemSelector":  itemSelector,
		"titleSelector": titleSelector,
		"parseMode":     "html",
	}
	configBytes, _ := json.Marshal(configMap)

	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "网页列表"
	}

	if err := tx.Exec(`INSERT INTO sources(id,name,kind,config,participation_mode,interval_minutes,reuse_window_minutes,site_fulltext,syndicate_fulltext,enabled,health,next_fetch_at,collector_identity) VALUES(?,?,'web_list',?::jsonb,'isolated',60,10,false,false,false,'paused',now(),?) ON CONFLICT DO NOTHING`, id, name, string(configBytes), identity).Error; err != nil {
		return "", err
	}

	if err := tx.Raw(`SELECT id FROM sources WHERE collector_identity=?`, identity).Row().Scan(&id); err != nil {
		return "", err
	}

	return id, nil
}

/** resolveRSSSource resolves or inserts a standard RSS/Atom source. */
func resolveRSSSource(tx *gorm.DB, raw string) (string, error) {
	identity := identityFor(raw)
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('myhot:rss-identity'),hashtext(?))`, identity).Error; err != nil {
		return "", err
	}

	var id string
	err := tx.Raw(`SELECT id FROM sources WHERE kind='rss' AND collector_identity=? FOR UPDATE`, identity).Row().Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	var legacy []struct {
		ID     string `gorm:"column:id"`
		Config []byte `gorm:"column:config"`
	}
	if err := tx.Raw(`SELECT id,config FROM sources WHERE kind='rss' AND collector_identity IS NULL ORDER BY id`).Scan(&legacy).Error; err != nil {
		return "", err
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
			return "", err
		}

		return id, nil
	}

	id = "rss-" + identity[:32]
	config, _ := json.Marshal(map[string]string{
		"feedUrl": strings.TrimSpace(raw),
	})

	if err := tx.Exec(`INSERT INTO sources(id,name,kind,config,participation_mode,interval_minutes,reuse_window_minutes,site_fulltext,syndicate_fulltext,enabled,health,next_fetch_at,collector_identity) VALUES(?,?,'rss',?::jsonb,'isolated',60,10,false,false,false,'paused',now(),?) ON CONFLICT DO NOTHING`, id, "个人 RSS", string(config), identity).Error; err != nil {
		return "", err
	}

	if err := tx.Raw(`SELECT id FROM sources WHERE collector_identity=?`, identity).Row().Scan(&id); err != nil {
		return "", err
	}

	return id, nil
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

/** recompute recalibrates schedule and enabled flags on the source record. */
func recompute(tx *gorm.DB, sourceID string, immediate bool) error {
	var lockedID string
	if err := tx.Raw(`SELECT id FROM sources WHERE id=? FOR UPDATE`, sourceID).Row().Scan(&lockedID); err != nil {
		return err
	}

	var values struct {
		Count    int `gorm:"column:count"`
		Interval int `gorm:"column:interval"`
		Window   int `gorm:"column:window"`
	}
	if err := tx.Raw(`SELECT count(*) AS count,coalesce(min(interval_minutes),60) AS interval,coalesce(min(reuse_window_minutes),10) AS window FROM user_source_subscriptions WHERE source_id=? AND enabled`, sourceID).Scan(&values).Error; err != nil {
		return err
	}

	if values.Count == 0 {
		return tx.Exec(`UPDATE sources SET enabled=false,health='paused',next_fetch_at=NULL,updated_at=now() WHERE id=?`, sourceID).Error
	}

	period := max(values.Interval, values.Window)
	next := "next_fetch_at"
	if immediate {
		next = "now()"
	}

	return tx.Exec(fmt.Sprintf(`UPDATE sources SET enabled=true,interval_minutes=?,reuse_window_minutes=?,health=CASE WHEN health='paused' THEN 'unknown' ELSE health END,next_fetch_at=%s,updated_at=now() WHERE id=?`, next), period, values.Window, sourceID).Error
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
