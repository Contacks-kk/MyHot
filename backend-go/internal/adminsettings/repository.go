package adminsettings

import (
	"context"
	"encoding/json"
	"errors"

	"aihot/backend-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormRepository struct {
	db              *gorm.DB
	contactDefaults Contact
}

func NewRepository(db *gorm.DB, contactDefaults ...Contact) *GormRepository {
	repository := &GormRepository{db: db}
	if len(contactDefaults) > 0 {
		repository.contactDefaults = contactDefaults[0]
	}
	return repository
}

func (r *GormRepository) Overview(ctx context.Context) (Overview, error) {
	contact, err := r.Contact(ctx)
	if err != nil {
		return Overview{}, err
	}

	var targets []Target
	err = r.db.WithContext(ctx).Table("notify_targets AS t").Select(`
		t.key, t.purpose, t.kind, t.enabled, t.enabled_at, t.config_ref, t.note, t.updated_at,
		(SELECT count(*)::int FROM deliveries d WHERE d.target_key = t.key AND d.created_at > now() - interval '7 days') AS deliveries_7d,
		(SELECT max(d.sent_at) FROM deliveries d WHERE d.target_key = t.key) AS last_sent_at`).
		Order("t.purpose, t.key").Scan(&targets).Error
	if err != nil {
		return Overview{}, err
	}
	if targets == nil {
		targets = []Target{}
	}

	var budgets []Budget
	err = r.db.WithContext(ctx).Table("budgets AS b").Select(`
		b.service, b.per_minute, b.per_hour, b.per_day, b.note, b.updated_at,
		(SELECT count(*)::int FROM receipt_attempts a WHERE a.service = b.service AND a.origin = 'live' AND a.started_at > now() - interval '1 day') AS used_day,
		(SELECT count(*)::int FROM receipt_attempts a WHERE a.service = b.service AND a.origin = 'live' AND a.started_at > now() - interval '1 hour') AS used_hour`).
		Order("b.service").Scan(&budgets).Error
	if err != nil {
		return Overview{}, err
	}
	if budgets == nil {
		budgets = []Budget{}
	}
	return Overview{Contact: contact, Targets: targets, Budgets: budgets}, nil
}

func (r *GormRepository) Contact(ctx context.Context) (Contact, error) {
	contact := r.contactDefaults
	var setting model.AppSetting
	result := r.db.WithContext(ctx).Select("value").First(&setting, "key = ?", "contact_qr")
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return Contact{}, result.Error
	}
	if result.RowsAffected != 0 && len(setting.Value) != 0 {
		if err := json.Unmarshal(setting.Value, &contact); err != nil {
			return Contact{}, err
		}
	}
	return contact, nil
}

func (r *GormRepository) SetTargetEnabled(ctx context.Context, key string, enabled bool, reason, actor string) (map[string]any, bool, error) {
	var response map[string]any
	found := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before model.NotifyTarget
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&before, "key = ?", key)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if result.Error != nil {
			return result.Error
		}
		found = true
		updates := map[string]any{"enabled": enabled, "updated_at": gorm.Expr("now()")}
		if enabled && !before.Enabled {
			updates["enabled_at"] = gorm.Expr("now()")
		}
		if err := tx.Model(&model.NotifyTarget{}).Where("key = ?", key).Updates(updates).Error; err != nil {
			return err
		}
		var after model.NotifyTarget
		if err := tx.Select("key", "enabled", "enabled_at").First(&after, "key = ?", key).Error; err != nil {
			return err
		}
		beforeJSON, _ := json.Marshal(map[string]any{"enabled": before.Enabled, "enabled_at": before.EnabledAt})
		afterJSON, _ := json.Marshal(map[string]any{"key": after.Key, "enabled": after.Enabled, "enabled_at": after.EnabledAt})
		action := "notify.disable"
		if enabled {
			action = "notify.enable"
		}
		if err := addAudit(tx, actor, action, "notify-target:"+key, reason, beforeJSON, afterJSON); err != nil {
			return err
		}
		response = map[string]any{"key": after.Key, "enabled": after.Enabled, "enabled_at": after.EnabledAt}
		return nil
	})
	return response, found && err == nil, err
}

func (r *GormRepository) UpdateBudget(ctx context.Context, service string, input BudgetInput, actor string) (map[string]any, error) {
	var response map[string]any
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Budget
		result := tx.First(&existing, "service = ?", service)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		var before any
		if result.RowsAffected != 0 {
			before = map[string]any{"per_minute": existing.PerMinute, "per_hour": existing.PerHour, "per_day": existing.PerDay}
		}
		budget := model.Budget{Service: service, PerMinute: input.PerMinute, PerHour: input.PerHour, PerDay: input.PerDay, Note: stringPtr(input.Reason)}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "service"}}, DoUpdates: clause.Assignments(map[string]any{
			"per_minute": input.PerMinute, "per_hour": input.PerHour, "per_day": input.PerDay, "note": input.Reason, "updated_at": gorm.Expr("now()"),
		})}).Create(&budget).Error; err != nil {
			return err
		}
		after := map[string]any{"service": service, "per_minute": input.PerMinute, "per_hour": input.PerHour, "per_day": input.PerDay}
		beforeJSON, _ := json.Marshal(before)
		afterJSON, _ := json.Marshal(after)
		if err := addAudit(tx, actor, "budget.update", "budget:"+service, input.Reason, beforeJSON, afterJSON); err != nil {
			return err
		}
		response = after
		return nil
	})
	return response, err
}

func (r *GormRepository) ReplaceContactQR(ctx context.Context, slot, imageURL, actor string) (Contact, error) {
	contact := r.contactDefaults
	var out Contact
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "admin-contact-qr").Error; err != nil {
			return err
		}
		var setting model.AppSetting
		result := tx.Select("value").First(&setting, "key = ?", "contact_qr")
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if result.RowsAffected > 0 && len(setting.Value) > 0 {
			if err := json.Unmarshal(setting.Value, &contact); err != nil {
				return err
			}
		}
		var before any
		if slot == "wechatQr" {
			before = contact.WechatQR
			contact.WechatQR = &imageURL
		} else {
			before = contact.FeishuQR
			contact.FeishuQR = &imageURL
		}
		encoded, err := json.Marshal(contact)
		if err != nil {
			return err
		}
		updatedBy := actor
		setting = model.AppSetting{Key: "contact_qr", Value: encoded, UpdatedBy: &updatedBy}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{
			"value": encoded, "updated_by": actor, "updated_at": gorm.Expr("now()"),
		})}).Create(&setting).Error; err != nil {
			return err
		}
		var beforeValue any
		if before != nil {
			beforeValue = before
		}
		beforeJSON, _ := json.Marshal(map[string]any{slot: beforeValue})
		afterJSON, _ := json.Marshal(map[string]any{slot: imageURL})
		subject := "settings:contact_qr"
		if err := tx.Create(&model.AdminAuditLog{Actor: actor, Action: "settings.contact_qr", Subject: &subject, Before: beforeJSON, After: afterJSON}).Error; err != nil {
			return err
		}
		out = contact
		return nil
	})
	return out, err
}

func addAudit(tx *gorm.DB, actor, action, subject, reason string, before, after []byte) error {
	return tx.Create(&model.AdminAuditLog{Actor: actor, Action: action, Subject: stringPtr(subject), Reason: stringPtr(reason), Before: before, After: after}).Error
}

func stringPtr(value string) *string { return &value }
