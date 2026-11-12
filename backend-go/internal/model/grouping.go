package model

import (
	"encoding/json"
	"time"
)

type Fact struct {
	ID         int64      `gorm:"column:id;primaryKey;autoIncrement"`
	PublicID   string     `gorm:"column:public_id"`
	StoryID    *int64     `gorm:"column:story_id"`
	Title      string     `gorm:"column:title"`
	Subject    *string    `gorm:"column:subject"`
	Action     *string    `gorm:"column:action"`
	Object     *string    `gorm:"column:object"`
	Conditions *string    `gorm:"column:conditions"`
	OccurredAt *time.Time `gorm:"column:occurred_at"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
}

func (Fact) TableName() string { return "facts" }

type FactArticle struct {
	FactID    int64     `gorm:"column:fact_id;primaryKey"`
	ArticleID string    `gorm:"column:article_id;primaryKey"`
	Role      string    `gorm:"column:role"`
	Evidence  *string   `gorm:"column:evidence"`
	Manual    bool      `gorm:"column:manual"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (FactArticle) TableName() string { return "fact_articles" }

type StorySignal struct {
	StoryID        int64     `gorm:"column:story_id;primaryKey"`
	ArticleID      string    `gorm:"column:article_id;primaryKey"`
	ParticipantKey string    `gorm:"column:participant_key"`
	SourceID       string    `gorm:"column:source_id"`
	Kind           string    `gorm:"column:kind"`
	ObservedAt     time.Time `gorm:"column:observed_at"`
}

func (StorySignal) TableName() string { return "story_signals" }

type GroupingDecision struct {
	ID         int64           `gorm:"column:id;primaryKey;autoIncrement"`
	ArticleID  string          `gorm:"column:article_id"`
	FactID     *int64          `gorm:"column:fact_id"`
	StoryID    *int64          `gorm:"column:story_id"`
	Verdict    string          `gorm:"column:verdict"`
	Candidates json.RawMessage `gorm:"column:candidates;type:jsonb"`
	ReceiptID  *int64          `gorm:"column:receipt_id"`
	CreatedAt  time.Time       `gorm:"column:created_at"`
}

func (GroupingDecision) TableName() string { return "grouping_decisions" }
