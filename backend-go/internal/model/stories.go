package model

import "time"

type Story struct {
	ID              int64      `gorm:"column:id;primaryKey"`
	PublicID        string     `gorm:"column:public_id"`
	Title           string     `gorm:"column:title"`
	FirstReportAt   *time.Time `gorm:"column:first_report_at"`
	LatestAt        *time.Time `gorm:"column:latest_at"`
	Digest          *string    `gorm:"column:digest"`
	DigestUpdatedAt *time.Time `gorm:"column:digest_updated_at"`
	Latest          *string    `gorm:"column:latest"`
	MergedInto      *int64     `gorm:"column:merged_into"`
	Version         int        `gorm:"column:version"`
	Origin          string     `gorm:"column:origin"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (Story) TableName() string { return "stories" }

type StoryDigest struct {
	StoryID    int64     `gorm:"column:story_id;primaryKey"`
	Version    int       `gorm:"column:version;primaryKey"`
	Digest     string    `gorm:"column:digest"`
	Latest     *string   `gorm:"column:latest"`
	ReceiptID  *int64    `gorm:"column:receipt_id"`
	ArticleIDs TextArray `gorm:"column:article_ids;type:text[]"`
	InputsHash *string   `gorm:"column:inputs_hash"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (StoryDigest) TableName() string { return "story_digests" }
