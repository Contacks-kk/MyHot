package model

import (
	"encoding/json"
	"time"
)

type ArticleProcessing struct {
	ID                 string     `gorm:"column:id;primaryKey"`
	ProcessingState    string     `gorm:"column:processing_state"`
	ProcessingAttempts int        `gorm:"column:processing_attempts"`
	ProcessingRetryAt  *time.Time `gorm:"column:processing_retry_at"`
	ProcessingError    *string    `gorm:"column:processing_error"`
	ProcessingQueuedAt *time.Time `gorm:"column:processing_queued_at"`
	DiscoveredAt       time.Time  `gorm:"column:discovered_at"`
}

func (ArticleProcessing) TableName() string { return "articles" }

type ArticleAnalysis struct {
	ID            int64           `gorm:"column:id;primaryKey"`
	ArticleID     string          `gorm:"column:article_id"`
	InputRevision int             `gorm:"column:input_revision"`
	Origin        string          `gorm:"column:origin"`
	Model         *string         `gorm:"column:model"`
	PromptVersion string          `gorm:"column:prompt_version"`
	ReceiptIDs    []int64         `gorm:"column:receipt_ids;type:bigint[]"`
	Relevance     string          `gorm:"column:relevance"`
	Category      *string         `gorm:"column:category"`
	Tags          []string        `gorm:"column:tags;type:text[]"`
	Subjects      []string        `gorm:"column:subjects;type:text[]"`
	TitleZh       string          `gorm:"column:title_zh"`
	SummaryZh     string          `gorm:"column:summary_zh"`
	ReasonZh      *string         `gorm:"column:reason_zh"`
	Score         *int            `gorm:"column:score"`
	Selected      bool            `gorm:"column:selected"`
	Output        json.RawMessage `gorm:"column:output;type:jsonb"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
}

func (ArticleAnalysis) TableName() string { return "analyses" }
