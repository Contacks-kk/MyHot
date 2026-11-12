// Package model contains database entities and transport-independent records.
// SQL migrations remain the schema source; these types are never AutoMigrated.
package model

import (
	"encoding/json"
	"time"
)

type UserAccount struct {
	ID           int64      `gorm:"column:id;primaryKey"`
	Email        string     `gorm:"column:email"`
	PasswordHash string     `gorm:"column:password_hash"`
	VerifiedAt   *time.Time `gorm:"column:verified_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
}

func (UserAccount) TableName() string { return "user_accounts" }

type UserSession struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	UserID    int64     `gorm:"column:user_id"`
	TokenHash string    `gorm:"column:token_hash"`
	CSRFToken string    `gorm:"column:csrf_token"`
	ExpiresAt time.Time `gorm:"column:expires_at"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (UserSession) TableName() string { return "user_sessions" }

type UserEmailToken struct {
	ID        int64      `gorm:"column:id;primaryKey"`
	UserID    int64      `gorm:"column:user_id"`
	TokenHash string     `gorm:"column:token_hash"`
	Purpose   string     `gorm:"column:purpose"`
	ExpiresAt time.Time  `gorm:"column:expires_at"`
	UsedAt    *time.Time `gorm:"column:used_at"`
	CreatedAt time.Time  `gorm:"column:created_at"`
}

func (UserEmailToken) TableName() string { return "user_email_tokens" }

type UserModelConfig struct {
	UserID           int64     `gorm:"column:user_id;primaryKey"`
	BaseURL          string    `gorm:"column:base_url"`
	APIKeyCiphertext string    `gorm:"column:api_key_ciphertext"`
	Model            string    `gorm:"column:model"`
	ConfigVersion    int       `gorm:"column:config_version"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (UserModelConfig) TableName() string { return "user_model_configs" }

type UserInterest struct {
	UserID            int64     `gorm:"column:user_id;primaryKey"`
	Keywords          TextArray `gorm:"column:keywords;type:text[]"`
	EntityIDs         TextArray `gorm:"column:entity_ids;type:text[]"`
	PreferenceVersion int       `gorm:"column:preference_version"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (UserInterest) TableName() string { return "user_interests" }

type UserArticleResult struct {
	ID             int64     `gorm:"column:id;primaryKey"`
	UserID         int64     `gorm:"column:user_id"`
	ArticleID      string    `gorm:"column:article_id"`
	InputRevision  int       `gorm:"column:input_revision"`
	Model          string    `gorm:"column:model"`
	RelevanceScore int       `gorm:"column:relevance_score"`
	Title          string    `gorm:"column:title"`
	Summary        string    `gorm:"column:summary"`
	Reason         *string   `gorm:"column:reason"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (UserArticleResult) TableName() string { return "user_article_results" }

type UserDailyReport struct {
	UserID     int64           `gorm:"column:user_id;primaryKey"`
	ReportDate time.Time       `gorm:"column:report_date;primaryKey"`
	Model      string          `gorm:"column:model"`
	Title      string          `gorm:"column:title"`
	Summary    string          `gorm:"column:summary"`
	Entries    json.RawMessage `gorm:"column:entries;type:jsonb"`
	CreatedAt  time.Time       `gorm:"column:created_at"`
}

func (UserDailyReport) TableName() string { return "user_daily_reports" }

type UserModelCallReceipt struct {
	ID          int64           `gorm:"column:id;primaryKey"`
	UserID      int64           `gorm:"column:user_id"`
	LogicalKey  string          `gorm:"column:logical_key"`
	Purpose     string          `gorm:"column:purpose"`
	ArticleID   *string         `gorm:"column:article_id"`
	Model       string          `gorm:"column:model"`
	Status      string          `gorm:"column:status"`
	RequestHash string          `gorm:"column:request_hash"`
	Usage       json.RawMessage `gorm:"column:usage;type:jsonb"`
	Error       *string         `gorm:"column:error"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	CompletedAt *time.Time      `gorm:"column:completed_at"`
}

func (UserModelCallReceipt) TableName() string { return "user_model_call_receipts" }

type PersonalizationScan struct {
	ArticleID          string    `gorm:"column:article_id;primaryKey"`
	InputRevision      int       `gorm:"column:input_revision;primaryKey"`
	ProcessedSourceIDs TextArray `gorm:"column:processed_source_ids;type:text[]"`
	ScannedAt          time.Time `gorm:"column:scanned_at"`
}

func (PersonalizationScan) TableName() string { return "personalization_scans" }

type PersonalizationRollout struct {
	ID          bool      `gorm:"column:id;primaryKey"`
	ActivatedAt time.Time `gorm:"column:activated_at"`
}

func (PersonalizationRollout) TableName() string { return "personalization_rollout" }

type Source struct {
	ID                 string          `gorm:"column:id;primaryKey"`
	Name               string          `gorm:"column:name"`
	Kind               string          `gorm:"column:kind"`
	ParticipationMode  string          `gorm:"column:participation_mode"`
	Config             json.RawMessage `gorm:"column:config;type:jsonb"`
	IconURL            *string         `gorm:"column:icon_url"`
	IconCheckedAt      *time.Time      `gorm:"column:icon_checked_at"`
	Enabled            bool            `gorm:"column:enabled"`
	IntervalMinutes    int             `gorm:"column:interval_minutes"`
	ReuseWindowMinutes int             `gorm:"column:reuse_window_minutes"`
	FailCount          int             `gorm:"column:fail_count"`
	Health             string          `gorm:"column:health"`
	NextFetchAt        *time.Time      `gorm:"column:next_fetch_at"`
	LastFetchAt        *time.Time      `gorm:"column:last_fetch_at"`
	LastOKAt           *time.Time      `gorm:"column:last_ok_at"`
	LastError          *string         `gorm:"column:last_error"`
	Cursor             json.RawMessage `gorm:"column:cursor;type:jsonb"`
	UpdatedAt          time.Time       `gorm:"column:updated_at"`
}

func (Source) TableName() string { return "sources" }

type FetchRun struct {
	ID         int64           `gorm:"column:id;primaryKey"`
	SourceID   string          `gorm:"column:source_id"`
	Status     string          `gorm:"column:status"`
	FinishedAt *time.Time      `gorm:"column:finished_at"`
	FoundCount int             `gorm:"column:found_count"`
	NewCount   int             `gorm:"column:new_count"`
	Error      *string         `gorm:"column:error"`
	Detail     json.RawMessage `gorm:"column:detail;type:jsonb"`
}

func (FetchRun) TableName() string { return "fetch_runs" }

type Article struct {
	ID             string          `gorm:"column:id;primaryKey"`
	SourceID       string          `gorm:"column:source_id"`
	IdentityKey    string          `gorm:"column:identity_key"`
	URL            string          `gorm:"column:url"`
	Title          string          `gorm:"column:title"`
	Author         *string         `gorm:"column:author"`
	PublishedAt    *time.Time      `gorm:"column:published_at"`
	PublishedClaim *time.Time      `gorm:"column:published_at_claim"`
	DiscoveredAt   time.Time       `gorm:"column:discovered_at"`
	TimelineAt     time.Time       `gorm:"column:timeline_at"`
	Backfill       bool            `gorm:"column:backfill"`
	BackfillReason *string         `gorm:"column:backfill_reason"`
	ContentHash    *string         `gorm:"column:content_hash"`
	Excerpt        *string         `gorm:"column:excerpt"`
	BodyText       *string         `gorm:"column:body_text"`
	BodyHTML       *string         `gorm:"column:body_html"`
	Language       *string         `gorm:"column:language"`
	Media          json.RawMessage `gorm:"column:media;type:jsonb"`
	XPost          json.RawMessage `gorm:"column:x_post;type:jsonb"`
	Raw            json.RawMessage `gorm:"column:raw;type:jsonb"`
	BodyStatus     string          `gorm:"column:body_status"`
	Revision       int             `gorm:"column:revision"`
	CreatedAt      time.Time       `gorm:"column:created_at"`
	UpdatedAt      time.Time       `gorm:"column:updated_at"`
}

func (Article) TableName() string { return "articles" }

type IngestEvent struct {
	ID        int64           `gorm:"column:id;primaryKey"`
	Client    string          `gorm:"column:client"`
	Kind      string          `gorm:"column:kind"`
	Status    string          `gorm:"column:status"`
	Summary   json.RawMessage `gorm:"column:summary;type:jsonb"`
	CreatedAt time.Time       `gorm:"column:created_at"`
}

func (IngestEvent) TableName() string { return "ingest_events" }

type ArticleRevision struct {
	ArticleID   string  `gorm:"column:article_id;primaryKey"`
	Revision    int     `gorm:"column:revision;primaryKey"`
	ContentHash string  `gorm:"column:content_hash"`
	Title       string  `gorm:"column:title"`
	BodyText    *string `gorm:"column:body_text"`
}

func (ArticleRevision) TableName() string { return "article_revisions" }

type ArticleDiscovery struct {
	ArticleID    string    `gorm:"column:article_id;primaryKey"`
	SourceID     string    `gorm:"column:source_id;primaryKey"`
	Via          string    `gorm:"column:via;primaryKey"`
	DiscoveredAt time.Time `gorm:"column:discovered_at"`
}

func (ArticleDiscovery) TableName() string { return "article_discoveries" }
