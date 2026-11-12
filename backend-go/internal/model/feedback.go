package model

import "time"

type Feedback struct {
	ID            int64      `gorm:"column:id;primaryKey"`
	Content       string     `gorm:"column:content"`
	Email         *string    `gorm:"column:email"`
	PageURL       *string    `gorm:"column:page_url"`
	ScreenshotKey *string    `gorm:"column:screenshot_key"`
	SourceHash    string     `gorm:"column:source_hash"`
	Status        string     `gorm:"column:status"`
	Note          *string    `gorm:"column:note"`
	ForwardedAt   *time.Time `gorm:"column:forwarded_at"`
	ForwardError  *string    `gorm:"column:forward_error"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (Feedback) TableName() string { return "feedback" }

type FeedbackBan struct {
	SourceHash string    `gorm:"column:source_hash;primaryKey"`
	Reason     *string   `gorm:"column:reason"`
	CreatedBy  *string   `gorm:"column:created_by"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (FeedbackBan) TableName() string { return "feedback_bans" }
