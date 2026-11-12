package model

import "time"

type Publication struct {
	ArticleID          string     `gorm:"column:article_id;primaryKey" db:"article_id"`
	AnalysisID         *int64     `gorm:"column:analysis_id" db:"analysis_id"`
	Revision           int        `gorm:"column:revision" db:"revision"`
	Visibility         string     `gorm:"column:visibility" db:"visibility"`
	Eligible           bool       `gorm:"column:eligible" db:"eligible"`
	Selected           bool       `gorm:"column:selected" db:"selected"`
	SelectionCandidate bool       `gorm:"column:selection_candidate" db:"selection_candidate"`
	Title              string     `gorm:"column:title" db:"title"`
	OriginalTitle      *string    `gorm:"column:original_title" db:"original_title"`
	Summary            *string    `gorm:"column:summary" db:"summary"`
	Reason             *string    `gorm:"column:reason" db:"reason"`
	Category           *string    `gorm:"column:category" db:"category"`
	Tags               []string   `gorm:"column:tags;type:text[]" db:"tags"`
	Score              *float64   `gorm:"column:score" db:"score"`
	SourceID           string     `gorm:"column:source_id" db:"source_id"`
	Channel            string     `gorm:"column:channel" db:"channel"`
	FirstParty         bool       `gorm:"column:first_party" db:"first_party"`
	URL                string     `gorm:"column:url" db:"url"`
	PublishedAt        *time.Time `gorm:"column:published_at" db:"published_at"`
	DiscoveredAt       time.Time  `gorm:"column:discovered_at" db:"discovered_at"`
	TimelineAt         time.Time  `gorm:"column:timeline_at" db:"timeline_at"`
	Backfill           bool       `gorm:"column:backfill" db:"backfill"`
	SelectedReadyAt    *time.Time `gorm:"column:selected_ready_at" db:"selected_ready_at"`
	VisibleAfter       *time.Time `gorm:"column:visible_after" db:"visible_after"`
	BodyMode           string     `gorm:"column:body_mode" db:"body_mode"`
	Syndicate          bool       `gorm:"column:syndicate" db:"syndicate"`
	Indexable          bool       `gorm:"column:indexable" db:"indexable"`
	StoryID            *int64     `gorm:"column:story_id" db:"story_id"`
	FactID             *int64     `gorm:"column:fact_id" db:"fact_id"`
	SearchText         string     `gorm:"column:search_text" db:"search_text"`
	SortAt             time.Time  `gorm:"column:sort_at" db:"sort_at"`
	Seat               bool       `gorm:"column:seat" db:"seat"`
	SEOIndexedAt       *time.Time `gorm:"column:seo_indexed_at" db:"seo_indexed_at"`
	SEOExcludedAt      *time.Time `gorm:"column:seo_excluded_at" db:"seo_excluded_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at" db:"updated_at"`
}

func (Publication) TableName() string { return "publications" }

type SelectedLedger struct {
	Sequence  int64     `gorm:"column:seq;primaryKey" db:"seq"`
	ArticleID string    `gorm:"column:article_id" db:"article_id"`
	Operation string    `gorm:"column:op" db:"op"`
	ChangedAt time.Time `gorm:"column:changed_at" db:"changed_at"`
	VisibleAt time.Time `gorm:"column:visible_at" db:"visible_at"`
	Payload   []byte    `gorm:"column:payload;type:jsonb" db:"payload"`
}

func (SelectedLedger) TableName() string { return "selected_ledger" }

type SelectedState struct {
	ArticleID    string  `gorm:"column:article_id;primaryKey" db:"article_id"`
	InSet        bool    `gorm:"column:in_set" db:"in_set"`
	PayloadHash  *string `gorm:"column:payload_hash" db:"payload_hash"`
	LastSequence int64   `gorm:"column:last_seq" db:"last_seq"`
}

func (SelectedState) TableName() string { return "selected_state" }
