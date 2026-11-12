package model

import (
	"encoding/json"
	"time"
)

type PaidReceipt struct {
	ID          int64           `gorm:"column:id;primaryKey"`
	LogicalKey  string          `gorm:"column:logical_key"`
	Service     string          `gorm:"column:service"`
	Model       *string         `gorm:"column:model"`
	Purpose     string          `gorm:"column:purpose"`
	Subject     *string         `gorm:"column:subject"`
	Status      string          `gorm:"column:status"`
	Request     json.RawMessage `gorm:"column:request;type:jsonb"`
	Response    json.RawMessage `gorm:"column:response;type:jsonb"`
	RequestID   *string         `gorm:"column:request_id"`
	Usage       json.RawMessage `gorm:"column:usage;type:jsonb"`
	Cost        *float64        `gorm:"column:cost"`
	Currency    *string         `gorm:"column:currency"`
	CostBasis   *string         `gorm:"column:cost_basis"`
	Error       *string         `gorm:"column:error"`
	Attempts    int             `gorm:"column:attempts"`
	Origin      string          `gorm:"column:origin"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	ReceivedAt  *time.Time      `gorm:"column:received_at"`
	CompletedAt *time.Time      `gorm:"column:completed_at"`
	UpdatedAt   time.Time       `gorm:"column:updated_at"`
}

func (PaidReceipt) TableName() string { return "receipts" }

type PaidReceiptAttempt struct {
	ID         int64           `gorm:"column:id;primaryKey"`
	ReceiptID  int64           `gorm:"column:receipt_id"`
	Attempt    int             `gorm:"column:attempt"`
	Service    string          `gorm:"column:service"`
	Model      *string         `gorm:"column:model"`
	Origin     string          `gorm:"column:origin"`
	Status     string          `gorm:"column:status"`
	RequestID  *string         `gorm:"column:request_id"`
	Usage      json.RawMessage `gorm:"column:usage;type:jsonb"`
	Cost       *float64        `gorm:"column:cost"`
	Currency   *string         `gorm:"column:currency"`
	CostBasis  *string         `gorm:"column:cost_basis"`
	LatencyMS  *int            `gorm:"column:latency_ms"`
	Error      *string         `gorm:"column:error"`
	StartedAt  time.Time       `gorm:"column:started_at"`
	FinishedAt *time.Time      `gorm:"column:finished_at"`
}

func (PaidReceiptAttempt) TableName() string { return "receipt_attempts" }

type ServiceBudget struct {
	Service   string `gorm:"column:service;primaryKey"`
	PerMinute int    `gorm:"column:per_minute"`
	PerHour   int    `gorm:"column:per_hour"`
	PerDay    int    `gorm:"column:per_day"`
}

func (ServiceBudget) TableName() string { return "budgets" }
