package model

import "time"

type UserSourceSubscription struct {
	ID                 int64     `gorm:"column:id;primaryKey"`
	UserID             int64     `gorm:"column:user_id"`
	SourceID           string    `gorm:"column:source_id"`
	Name               string    `gorm:"column:name"`
	Enabled            bool      `gorm:"column:enabled"`
	FilterMode         string    `gorm:"column:filter_mode"`
	IntervalMinutes    int       `gorm:"column:interval_minutes"`
	ReuseWindowMinutes int       `gorm:"column:reuse_window_minutes"`
	ColdStartPending   bool      `gorm:"column:cold_start_pending"`
	ActivatedAt        time.Time `gorm:"column:activated_at"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
}

func (UserSourceSubscription) TableName() string { return "user_source_subscriptions" }
