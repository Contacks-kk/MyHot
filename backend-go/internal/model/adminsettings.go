package model

import (
	"encoding/json"
	"time"
)

type NotifyTarget struct {
	Key       string     `gorm:"column:key;primaryKey"`
	Purpose   string     `gorm:"column:purpose"`
	Kind      string     `gorm:"column:kind"`
	Enabled   bool       `gorm:"column:enabled"`
	EnabledAt *time.Time `gorm:"column:enabled_at"`
	ConfigRef *string    `gorm:"column:config_ref"`
	Note      *string    `gorm:"column:note"`
	UpdatedAt time.Time  `gorm:"column:updated_at"`
}

func (NotifyTarget) TableName() string { return "notify_targets" }

type Budget struct {
	Service   string    `gorm:"column:service;primaryKey"`
	PerMinute int       `gorm:"column:per_minute"`
	PerHour   int       `gorm:"column:per_hour"`
	PerDay    int       `gorm:"column:per_day"`
	Note      *string   `gorm:"column:note"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (Budget) TableName() string { return "budgets" }

type AppSetting struct {
	Key       string          `gorm:"column:key;primaryKey"`
	Value     json.RawMessage `gorm:"column:value;type:jsonb"`
	UpdatedBy *string         `gorm:"column:updated_by"`
	UpdatedAt time.Time       `gorm:"column:updated_at"`
}

func (AppSetting) TableName() string { return "settings" }
