package model

import "time"

/** Label represents a canonical or candidate topic/concept tag. */
type Label struct {
	ID          string    `gorm:"column:id;primaryKey"`
	Name        string    `gorm:"column:name"`
	Category    string    `gorm:"column:category"`
	IsCanonical bool      `gorm:"column:is_canonical"`
	UseCount    int       `gorm:"column:use_count"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (Label) TableName() string { return "labels" }

/** SourceLabel models the many-to-many relationship between sources and labels. */
type SourceLabel struct {
	SourceID   string    `gorm:"column:source_id;primaryKey"`
	LabelID    string    `gorm:"column:label_id;primaryKey"`
	Confidence float32   `gorm:"column:confidence"`
	Origin     string    `gorm:"column:origin"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (SourceLabel) TableName() string { return "source_labels" }
