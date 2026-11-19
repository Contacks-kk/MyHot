package model

import (
	"encoding/json"
	"time"
)

type SelectBenchRun struct {
	ID            string          `gorm:"column:id;primaryKey"`
	Label         string          `gorm:"column:label"`
	Split         *string         `gorm:"column:split"`
	SampleSize    int             `gorm:"column:sample_size"`
	Seed          *int64          `gorm:"column:seed"`
	PromptVersion *string         `gorm:"column:prompt_version"`
	Models        TextArray       `gorm:"column:models;type:text[]"`
	Summary       json.RawMessage `gorm:"column:summary;type:jsonb"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
	ImportedBy    *string         `gorm:"column:imported_by"`
}

func (SelectBenchRun) TableName() string { return "selectbench_runs" }

type SelectBenchResult struct {
	RunID     string  `gorm:"column:run_id;primaryKey"`
	Model     string  `gorm:"column:model;primaryKey"`
	CaseID    string  `gorm:"column:case_id;primaryKey"`
	Title     string  `gorm:"column:title"`
	Stratum   *string `gorm:"column:stratum"`
	Gold      string  `gorm:"column:gold"`
	Decision  *string `gorm:"column:decision"`
	Score     *int    `gorm:"column:score"`
	Relevance *string `gorm:"column:relevance"`
	Category  *string `gorm:"column:category"`
	Reason    *string `gorm:"column:reason"`
	ReceiptID *int64  `gorm:"column:receipt_id"`
	Error     *string `gorm:"column:error"`
}

func (SelectBenchResult) TableName() string { return "selectbench_results" }
