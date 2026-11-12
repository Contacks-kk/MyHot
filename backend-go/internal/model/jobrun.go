package model

import (
	"encoding/json"
	"time"
)

type JobRun struct {
	ID         int64           `gorm:"column:id;primaryKey"`
	Job        string          `gorm:"column:job"`
	StartedAt  time.Time       `gorm:"column:started_at"`
	FinishedAt *time.Time      `gorm:"column:finished_at"`
	Status     string          `gorm:"column:status"`
	Detail     json.RawMessage `gorm:"column:detail;type:jsonb"`
	Error      *string         `gorm:"column:error"`
}

func (JobRun) TableName() string { return "job_runs" }
