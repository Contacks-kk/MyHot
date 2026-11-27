package account

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/model"
	"aihot/backend-go/internal/sourceschedule"
	"gorm.io/gorm"
)

var ErrNotFound = errors.New("account record not found")

// Repository owns PostgreSQL account and email-token persistence.
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

/** CreateVerifiedAccount inserts a newly verified user account and rejects duplicate emails. */
func (r *Repository) CreateVerifiedAccount(ctx context.Context, email, passwordHash string) (int64, error) {
	var id int64
	err := r.db.WithContext(ctx).Raw(`INSERT INTO user_accounts(email,password_hash,verified_at) VALUES(?,?,now()) ON CONFLICT DO NOTHING RETURNING id`, email, passwordHash).Row().Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrDuplicateAccount
	}
	if err != nil {
		return 0, err
	}
	return id, nil
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

func (r *Repository) DeleteAccount(ctx context.Context, userID int64, schedule config.SourceSchedule) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sourceIDs []string
		if err := tx.Raw(`SELECT source_id FROM user_source_subscriptions WHERE user_id=? ORDER BY source_id`, userID).Scan(&sourceIDs).Error; err != nil {
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
			if err := sourceschedule.Recompute(tx, sourceID, schedule); err != nil {
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

/** ResetPassword consumes a password-reset token, updates the user password hash, and returns the user ID. */
func (r *Repository) ResetPassword(ctx context.Context, tokenHash, passwordHash string) (int64, error) {
	var userID int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`UPDATE user_email_tokens SET used_at=now() WHERE token_hash=? AND purpose='password_reset' AND used_at IS NULL AND expires_at>now() RETURNING user_id`, tokenHash).Row().Scan(&userID); err != nil {
			return notFound(err)
		}
		return tx.Model(&model.UserAccount{}).Where("id = ?", userID).Updates(map[string]any{
			"password_hash": passwordHash,
			"updated_at":    gorm.Expr("now()"),
		}).Error
	})
	return userID, err
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
