package adminsettings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openSettingsIntegrationRepository(t *testing.T) (*gorm.DB, *GormRepository) {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	orm, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return orm, NewRepository(orm)
}

func TestReplaceContactQRUpdatesSettingAndAuditInOneTransaction(t *testing.T) {
	orm, repository := openSettingsIntegrationRepository(t)
	ctx := context.Background()
	actor := fmt.Sprintf("contact-qr-test-%d", time.Now().UnixNano())
	var previous model.AppSetting
	lookup := orm.First(&previous, "key = ?", "contact_qr")
	if lookup.Error != nil && lookup.Error != gorm.ErrRecordNotFound {
		t.Fatal(lookup.Error)
	}
	previousExists := lookup.RowsAffected > 0
	t.Cleanup(func() {
		_ = orm.Where("actor = ? AND action = ?", actor, "settings.contact_qr").Delete(&model.AdminAuditLog{}).Error
		if previousExists {
			_ = orm.Model(&model.AppSetting{}).Where("key = ?", "contact_qr").Updates(map[string]any{"value": previous.Value, "updated_by": previous.UpdatedBy, "updated_at": previous.UpdatedAt}).Error
		} else {
			_ = orm.Delete(&model.AppSetting{}, "key = ?", "contact_qr").Error
		}
	})
	if err := orm.Exec("INSERT INTO settings(key,value) VALUES(?,?::jsonb) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", "contact_qr", `{"wechatQr":"/contact/old.png","feishuQr":"/contact/team.png"}`).Error; err != nil {
		t.Fatal(err)
	}
	got, err := repository.ReplaceContactQR(ctx, "wechatQr", "/contact/qr-wechat-1234abcd.png", actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.WechatQR == nil || *got.WechatQR != "/contact/qr-wechat-1234abcd.png" || got.FeishuQR == nil || *got.FeishuQR != "/contact/team.png" {
		t.Fatalf("updated contact = %#v", got)
	}
	var setting model.AppSetting
	if err := orm.First(&setting, "key = ?", "contact_qr").Error; err != nil {
		t.Fatal(err)
	}
	var stored Contact
	if err := json.Unmarshal(setting.Value, &stored); err != nil || stored.WechatQR == nil || *stored.WechatQR != *got.WechatQR || stored.FeishuQR == nil || *stored.FeishuQR != *got.FeishuQR || setting.UpdatedBy == nil || *setting.UpdatedBy != actor {
		t.Fatalf("stored contact setting = %#v, updated_by=%v err=%v", stored, setting.UpdatedBy, err)
	}
	var audit model.AdminAuditLog
	if err := orm.First(&audit, "actor = ? AND action = ? AND subject = ?", actor, "settings.contact_qr", "settings:contact_qr").Error; err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(audit.Before, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(audit.After, &after); err != nil {
		t.Fatal(err)
	}
	if before["wechatQr"] != "/contact/old.png" || after["wechatQr"] != "/contact/qr-wechat-1234abcd.png" {
		t.Fatalf("contact audit before=%v after=%v", before, after)
	}
}
