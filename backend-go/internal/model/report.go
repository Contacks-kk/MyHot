package model

import (
	"encoding/json"
	"time"
)

// Report maps the SQL-managed shared report table. Schema changes remain in migrations.
type Report struct {
	ID          int64           `gorm:"column:id;primaryKey"`
	Kind        string          `gorm:"column:kind"`
	Key         string          `gorm:"column:key"`
	WindowStart time.Time       `gorm:"column:window_start"`
	WindowEnd   time.Time       `gorm:"column:window_end"`
	Content     json.RawMessage `gorm:"column:content;type:jsonb"`
	GeneratedAt time.Time       `gorm:"column:generated_at"`
	Model       *string         `gorm:"column:model"`
	Revision    int             `gorm:"column:revision"`
	Origin      string          `gorm:"column:origin"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	UpdatedAt   time.Time       `gorm:"column:updated_at"`
}

func (Report) TableName() string { return "reports" }

type ReportRevision struct {
	ReportID    int64           `gorm:"column:report_id;primaryKey"`
	Revision    int             `gorm:"column:revision;primaryKey"`
	Content     json.RawMessage `gorm:"column:content;type:jsonb"`
	GeneratedAt time.Time       `gorm:"column:generated_at"`
	Reason      *string         `gorm:"column:reason"`
}

func (ReportRevision) TableName() string { return "report_revisions" }
