package model

import (
	"encoding/json"
	"time"
)

type AdminUser struct {
	ID            int64      `gorm:"column:id;primaryKey"`
	FeishuUnionID *string    `gorm:"column:feishu_union_id"`
	Email         *string    `gorm:"column:email"`
	DisplayName   *string    `gorm:"column:display_name"`
	Role          string     `gorm:"column:role;default:admin"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	LastLoginAt   *time.Time `gorm:"column:last_login_at"`
}

func (AdminUser) TableName() string { return "admin_users" }

type AdminSession struct {
	IDHash      string          `gorm:"column:id_hash;primaryKey"`
	UserID      int64           `gorm:"column:user_id"`
	CSRFToken   string          `gorm:"column:csrf_token"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	ExpiresAt   time.Time       `gorm:"column:expires_at"`
	UserAgent   *string         `gorm:"column:user_agent"`
	AuthMethod  *string         `gorm:"column:auth_method"`
	AuthBinding *string         `gorm:"column:auth_binding"`
	AuthClaims  json.RawMessage `gorm:"column:auth_claims;type:jsonb"`
}

func (AdminSession) TableName() string { return "admin_sessions" }

type AdminAuditLog struct {
	ID        int64           `gorm:"column:id;primaryKey"`
	Actor     string          `gorm:"column:actor"`
	Action    string          `gorm:"column:action"`
	Subject   *string         `gorm:"column:subject"`
	Reason    *string         `gorm:"column:reason"`
	Before    json.RawMessage `gorm:"column:before;type:jsonb"`
	After     json.RawMessage `gorm:"column:after;type:jsonb"`
	RequestID *string         `gorm:"column:request_id"`
	CreatedAt time.Time       `gorm:"column:created_at"`
}

func (AdminAuditLog) TableName() string { return "audit_log" }

type Delivery struct {
	ID        int64      `gorm:"column:id;primaryKey"`
	Status    string     `gorm:"column:status"`
	Response  *string    `gorm:"column:response"`
	SentAt    *time.Time `gorm:"column:sent_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at"`
}

func (Delivery) TableName() string { return "deliveries" }
