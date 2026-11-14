package adminauth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/** FindAccount looks up a user account by ID or normalized email. */
func (r *GormRepository) FindAccount(ctx context.Context, id int64, email string) (AdminAccount, error) {
	var account AdminAccount
	query := r.db.WithContext(ctx).Table("user_accounts").Select("id, email, password_hash, verified_at, is_admin")
	if id > 0 {
		query = query.Where("id = ?", id)
	} else {
		query = query.Where("lower(email) = ?", email)
	}
	result := query.Limit(1).Scan(&account)
	if result.Error != nil {
		return account, result.Error
	}
	if result.RowsAffected == 0 {
		return account, ErrNotFound
	}
	return account, nil
}

/** CreateAccountSession persists an independent admin session and audit entry. */
func (r *GormRepository) CreateAccountSession(ctx context.Context, account AdminAccount, tokenHash, csrf, binding string, expires time.Time, agent *string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current AdminAccount
		result := tx.Table("user_accounts").Clauses(clause.Locking{Strength: "SHARE"}).Select("id, email, password_hash, verified_at, is_admin").Where("id = ?", account.ID).Scan(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 || !current.IsAdmin || current.VerifiedAt == nil || current.PasswordHash != account.PasswordHash {
			return ErrLoginRejected
		}
		internalEmail := fmt.Sprintf("account-%d@admin.local", account.ID)
		user := model.AdminUser{Email: &internalEmail, DisplayName: &account.Email}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "email"}},
			DoUpdates: clause.Assignments(map[string]any{"last_login_at": gorm.Expr("now()"), "display_name": account.Email}),
		}).Create(&user).Error; err != nil {
			return err
		}
		claims, err := json.Marshal(accountClaims{AccountID: account.ID})
		if err != nil {
			return err
		}
		method := "password"
		if err := tx.Create(&model.AdminSession{
			IDHash:      tokenHash,
			UserID:      user.ID,
			CSRFToken:   csrf,
			ExpiresAt:   expires,
			UserAgent:   agent,
			AuthMethod:  &method,
			AuthBinding: &binding,
			AuthClaims:  claims,
		}).Error; err != nil {
			return err
		}
		return createAudit(tx, user.ID, map[string]string{"method": "account"})
	})
}

/** ListAccounts retrieves a page ordered by account ID. */
func (r *GormRepository) ListAccounts(ctx context.Context, after int64, limit int) ([]AdminAccount, error) {
	accounts := make([]AdminAccount, 0)
	err := r.db.WithContext(ctx).Table("user_accounts").Select("id, email, verified_at, is_admin").Where("id > ?", after).Order("id ASC").Limit(limit).Scan(&accounts).Error
	return accounts, err
}

/** SetAccountAdmin records the grant change and its audit entry atomically. */
func (r *GormRepository) SetAccountAdmin(ctx context.Context, actor, accountID int64, granted bool) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account AdminAccount
		lookup := tx.Table("user_accounts").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id, email").Where("id = ?", accountID).Scan(&account)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 0 {
			return ErrNotFound
		}
		result := tx.Table("user_accounts").Where("id = ?", accountID).Update("is_admin", granted)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		if !granted {
			if err := tx.Where("(auth_method = ? AND auth_claims->>'accountId' = ?) OR (auth_method = ? AND auth_claims->>'email' = lower(?))", "password", fmt.Sprint(accountID), "feishu", account.Email).Delete(&model.AdminSession{}).Error; err != nil {
				return err
			}
		}
		after, err := json.Marshal(map[string]bool{"isAdmin": granted})
		if err != nil {
			return err
		}
		subject := fmt.Sprintf("user:%d", accountID)
		return tx.Create(&model.AdminAuditLog{
			Actor:   fmt.Sprintf("admin:%d", actor),
			Action:  "user.admin_grant",
			Subject: &subject,
			After:   after,
		}).Error
	})
}
