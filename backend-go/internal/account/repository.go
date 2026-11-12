package account

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
)

var ErrNotFound = errors.New("account record not found")

// Repository owns account, session, and email-token persistence.
type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

type Session struct {
	ID    int64
	Email string
	CSRF  string
}

type LoginRecord struct {
	ID           int64
	PasswordHash string
	VerifiedAt   sql.NullTime
}

type AccountStatus struct {
	ID         int64
	VerifiedAt sql.NullTime
}

func (r *Repository) FindSession(ctx context.Context, tokenHash string) (Session, error) {
	var value Session
	err := r.db.WithContext(ctx).Raw(`SELECT u.id,u.email,us.csrf_token FROM user_sessions us JOIN user_accounts u ON u.id=us.user_id WHERE us.token_hash=? AND us.expires_at>now() AND u.verified_at IS NOT NULL`, tokenHash).Row().Scan(&value.ID, &value.Email, &value.CSRF)
	return value, notFound(err)
}

func (r *Repository) Register(ctx context.Context, email, passwordHash, tokenHash, csrf string, expiresAt time.Time) (int64, error) {
	var id int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Raw(`INSERT INTO user_accounts(email,password_hash,verified_at) VALUES(?,?,now()) ON CONFLICT DO NOTHING RETURNING id`, email, passwordHash).Row().Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDuplicateAccount
		}
		if err != nil {
			return err
		}
		return tx.Omit("CreatedAt").Create(&model.UserSession{UserID: id, TokenHash: tokenHash, CSRFToken: csrf, ExpiresAt: expiresAt}).Error
	})
	return id, err
}

func (r *Repository) LoginRecord(ctx context.Context, email string) (LoginRecord, error) {
	var value LoginRecord
	result := r.db.WithContext(ctx).Model(&model.UserAccount{}).Select("id", "password_hash", "verified_at").Where("lower(email)=lower(?)", email).Scan(&value)
	if result.Error != nil {
		return LoginRecord{}, result.Error
	}
	if result.RowsAffected == 0 {
		return LoginRecord{}, ErrNotFound
	}
	return value, nil
}

func (r *Repository) CreateSession(ctx context.Context, userID int64, tokenHash, csrf string, expiresAt time.Time) error {
	return r.db.WithContext(ctx).Omit("CreatedAt").Create(&model.UserSession{UserID: userID, TokenHash: tokenHash, CSRFToken: csrf, ExpiresAt: expiresAt}).Error
}

func (r *Repository) DeleteSession(ctx context.Context, userID int64, tokenHash string) error {
	return r.db.WithContext(ctx).Where("user_id = ? AND token_hash = ?", userID, tokenHash).Delete(&model.UserSession{}).Error
}

func (r *Repository) PasswordHash(ctx context.Context, userID int64) (string, error) {
	var hash string
	result := r.db.WithContext(ctx).Model(&model.UserAccount{}).Select("password_hash").Where("id = ?", userID).Scan(&hash)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", ErrNotFound
	}
	return hash, nil
}

func (r *Repository) DeleteAccount(ctx context.Context, userID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sourceIDs []string
		if err := tx.Raw(`SELECT source_id FROM user_source_subscriptions WHERE user_id=?`, userID).Scan(&sourceIDs).Error; err != nil {
			return err
		}
		var queueExists bool
		if err := tx.Raw("SELECT to_regclass('river_job') IS NOT NULL").Row().Scan(&queueExists); err != nil {
			return err
		}
		if queueExists {
			if err := tx.Exec(`DELETE FROM river_job WHERE kind IN ('myhot_personal_article','myhot_personal_triage','myhot_personal_daily','myhot_connection_test') AND args->>'userId'=?`, strconv.FormatInt(userID, 10)).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&model.UserAccount{}, "id = ?", userID).Error; err != nil {
			return err
		}
		for _, sourceID := range sourceIDs {
			var lockedID string
			if err := tx.Raw(`SELECT id FROM sources WHERE id=? FOR UPDATE`, sourceID).Row().Scan(&lockedID); err != nil {
				return err
			}
			var effective struct {
				Count    int `gorm:"column:count"`
				Interval int `gorm:"column:interval"`
				Window   int `gorm:"column:window"`
			}
			if err := tx.Raw(`SELECT count(*) AS count,coalesce(min(interval_minutes),60) AS interval,coalesce(min(reuse_window_minutes),10) AS window FROM user_source_subscriptions WHERE source_id=? AND enabled`, sourceID).Scan(&effective).Error; err != nil {
				return err
			}
			if effective.Count == 0 {
				if err := tx.Exec(`UPDATE sources SET enabled=false,health='paused',next_fetch_at=NULL,updated_at=now() WHERE id=?`, sourceID).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Exec(`UPDATE sources SET enabled=true,interval_minutes=?,reuse_window_minutes=?,next_fetch_at=now(),updated_at=now() WHERE id=?`, max(effective.Interval, effective.Window), effective.Window, sourceID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) VerifyEmail(ctx context.Context, email string) error {
	result := r.db.WithContext(ctx).Model(&model.UserAccount{}).Where("lower(email)=lower(?) AND verified_at IS NULL", email).Updates(map[string]any{"verified_at": gorm.Expr("now()"), "updated_at": gorm.Expr("now()")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) AccountByEmail(ctx context.Context, email string) (AccountStatus, error) {
	var value AccountStatus
	result := r.db.WithContext(ctx).Model(&model.UserAccount{}).Select("id", "verified_at").Where("lower(email)=lower(?)", email).Scan(&value)
	if result.Error != nil {
		return AccountStatus{}, result.Error
	}
	if result.RowsAffected == 0 {
		return AccountStatus{}, ErrNotFound
	}
	return value, nil
}

func (r *Repository) VerifiedAccountID(ctx context.Context, email string) (int64, error) {
	var id int64
	result := r.db.WithContext(ctx).Model(&model.UserAccount{}).Select("id").Where("lower(email)=lower(?) AND verified_at IS NOT NULL", email).Scan(&id)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, ErrNotFound
	}
	return id, nil
}

func (r *Repository) ReplaceEmailToken(ctx context.Context, userID int64, purpose, tokenHash string, expiresAt time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.UserEmailToken{}).Where("user_id = ? AND purpose = ? AND used_at IS NULL", userID, purpose).Update("used_at", gorm.Expr("now()")).Error; err != nil {
			return err
		}
		return tx.Omit("CreatedAt").Create(&model.UserEmailToken{UserID: userID, TokenHash: tokenHash, Purpose: purpose, ExpiresAt: expiresAt}).Error
	})
}

func (r *Repository) ResetPassword(ctx context.Context, tokenHash, passwordHash string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var userID int64
		if err := tx.Raw(`UPDATE user_email_tokens SET used_at=now() WHERE token_hash=? AND purpose='password_reset' AND used_at IS NULL AND expires_at>now() RETURNING user_id`, tokenHash).Row().Scan(&userID); err != nil {
			return notFound(err)
		}
		if err := tx.Model(&model.UserAccount{}).Where("id = ?", userID).Updates(map[string]any{"password_hash": passwordHash, "updated_at": gorm.Expr("now()")}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", userID).Delete(&model.UserSession{}).Error
	})
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
