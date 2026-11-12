package adminauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNotFound = errors.New("admin session not found")

type Repository interface {
	CreatePasswordSession(context.Context, string, string, string, time.Time, *string) (int64, error)
	CreateFeishuSession(context.Context, FeishuClaims, string, string, string, time.Time, *string) (int64, error)
	FindSession(context.Context, string) (SessionRecord, error)
	DeleteSession(context.Context, string) error
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

func (r *GormRepository) CreatePasswordSession(ctx context.Context, tokenHash, csrf, binding string, expiresAt time.Time, userAgent *string) (int64, error) {
	var userID int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lastLogin := time.Now()
		user := model.AdminUser{Email: stringPointer("admin@local"), DisplayName: stringPointer("管理员"), LastLoginAt: &lastLogin}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "email"}},
			DoUpdates: clause.Assignments(map[string]any{"last_login_at": gorm.Expr("now()")}),
		}).Create(&user).Error; err != nil {
			return err
		}
		userID = user.ID
		if err := createSession(tx, userID, tokenHash, csrf, expiresAt, userAgent, "password", binding, nil); err != nil {
			return err
		}
		return createAudit(tx, userID, map[string]string{"method": "password"})
	})
	return userID, err
}

func (r *GormRepository) CreateFeishuSession(ctx context.Context, claims FeishuClaims, tokenHash, csrf, binding string, expiresAt time.Time, userAgent *string) (int64, error) {
	var userID int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.AdminUser
		unionID, email := claimString(claims.UnionID), claimString(claims.Email)
		query := tx.Where("(? <> '' AND feishu_union_id = ?) OR (? <> '' AND email = ?)", unionID, unionID, email, email).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		var user model.AdminUser
		if query.RowsAffected > 0 {
			updates := map[string]any{"last_login_at": gorm.Expr("now()")}
			if unionID != "" {
				updates["feishu_union_id"] = gorm.Expr("coalesce(feishu_union_id, ?)", unionID)
			}
			if email != "" {
				updates["email"] = gorm.Expr("coalesce(email, ?)", email)
			}
			if claims.Name != nil {
				updates["display_name"] = gorm.Expr("coalesce(?, display_name)", *claims.Name)
			}
			if err := tx.Model(&model.AdminUser{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
				return err
			}
			userID = existing.ID
		} else {
			lastLogin := time.Now()
			user = model.AdminUser{LastLoginAt: &lastLogin}
			if claims.UnionID != nil {
				user.FeishuUnionID = claims.UnionID
			}
			if claims.Email != nil {
				user.Email = claims.Email
			}
			user.DisplayName = claims.Name
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			userID = user.ID
		}
		if err := createSession(tx, userID, tokenHash, csrf, expiresAt, userAgent, "feishu", binding, &claims); err != nil {
			return err
		}
		return createAudit(tx, userID, map[string]*string{"union_id": claims.UnionID})
	})
	return userID, err
}

func createSession(tx *gorm.DB, userID int64, tokenHash, csrf string, expiresAt time.Time, userAgent *string, method, binding string, claims *FeishuClaims) error {
	var rawClaims json.RawMessage
	if claims != nil {
		encoded, err := json.Marshal(claims.persisted())
		if err != nil {
			return err
		}
		rawClaims = encoded
	}
	return tx.Create(&model.AdminSession{
		IDHash: tokenHash, UserID: userID, CSRFToken: csrf, ExpiresAt: expiresAt, UserAgent: userAgent,
		AuthMethod: stringPointer(method), AuthBinding: stringPointer(binding), AuthClaims: rawClaims,
	}).Error
}

func (r *GormRepository) FindSession(ctx context.Context, hash string) (SessionRecord, error) {
	var row struct {
		UserID      int64
		CSRFToken   string
		Name        sql.NullString
		Email       sql.NullString
		AuthMethod  sql.NullString
		AuthBinding sql.NullString
		AuthClaims  []byte
	}
	result := r.db.WithContext(ctx).Table("admin_sessions AS s").
		Select("s.user_id, s.csrf_token, u.display_name AS name, u.email, s.auth_method, s.auth_binding, s.auth_claims").
		Joins("JOIN admin_users u ON u.id = s.user_id").
		Where("s.id_hash = ? AND s.expires_at > now()", hash).Limit(1).Scan(&row)
	if result.Error != nil {
		return SessionRecord{}, result.Error
	}
	if result.RowsAffected == 0 {
		return SessionRecord{}, ErrNotFound
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	} else if row.Email.Valid {
		name = row.Email.String
	} else {
		name = "admin:" + strconv.FormatInt(row.UserID, 10)
	}
	return SessionRecord{UserID: row.UserID, CSRF: row.CSRFToken, Name: name, AuthMethod: row.AuthMethod.String, AuthBinding: row.AuthBinding.String, AuthClaims: row.AuthClaims}, nil
}

func (r *GormRepository) DeleteSession(ctx context.Context, hash string) error {
	return r.db.WithContext(ctx).Where("id_hash = ?", hash).Delete(&model.AdminSession{}).Error
}

func stringPointer(value string) *string { return &value }

func claimString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func createAudit(tx *gorm.DB, userID int64, after any) error {
	encoded, err := json.Marshal(after)
	if err != nil {
		return err
	}
	return tx.Create(&model.AdminAuditLog{Actor: "admin:" + strconv.FormatInt(userID, 10), Action: "auth.login", After: encoded}).Error
}
