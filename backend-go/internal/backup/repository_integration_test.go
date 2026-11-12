package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func localBackupTestORM(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end with _test or _ci")
	}
	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestGormRepositoryAuditsDailyBackupAttemptsAndSummary(t *testing.T) {
	db := localBackupTestORM(t)
	ctx := context.Background()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(location)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 1)
	repository := NewRepository(db)
	if exists, err := repository.HasRun(ctx, start, end); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Skip("the isolated test database already has today's ops.backup run")
	}
	var previous json.RawMessage
	var previousExists bool
	if err := db.Raw("SELECT value FROM settings WHERE key='backup.last'").Row().Scan(&previous); err == nil {
		previousExists = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	var ids []int64
	t.Cleanup(func() {
		if len(ids) > 0 {
			_ = db.Where("id IN ?", ids).Delete(&model.JobRun{}).Error
		}
		if previousExists {
			_ = db.Exec("UPDATE settings SET value=?::jsonb WHERE key='backup.last'", string(previous)).Error
		} else {
			_ = db.Exec("DELETE FROM settings WHERE key='backup.last'").Error
		}
	})
	first, created, err := repository.BeginRun(ctx, start, end)
	if err != nil || !created || first == 0 {
		t.Fatalf("first backup attempt: id=%d created=%t err=%v", first, created, err)
	}
	ids = append(ids, first)
	if err := repository.FailRun(ctx, first, "fixture archive failure"); err != nil {
		t.Fatal(err)
	}
	retry, created, err := repository.BeginRun(ctx, start, end)
	if err != nil || !created || retry == 0 {
		t.Fatalf("one retry after failed attempt: id=%d created=%t err=%v", retry, created, err)
	}
	ids = append(ids, retry)
	if err := repository.SaveLast(ctx, json.RawMessage(`{"at":"2026-11-01T04:00:00.000Z","uploaded":true,"objects":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteRun(ctx, retry, json.RawMessage(`{"uploaded":true,"objects":[]}`)); err != nil {
		t.Fatal(err)
	}
	if exists, err := repository.HasRun(ctx, start, end); err != nil || !exists {
		t.Fatalf("completed backup was not recognized: exists=%t err=%v", exists, err)
	}
	var value model.JobRun
	if err := db.First(&value, retry).Error; err != nil {
		t.Fatal(err)
	}
	if value.Status != "ok" || string(value.Detail) != `{"uploaded":true,"objects":[]}` {
		t.Fatalf("backup audit was not persisted: %#v", value)
	}
}
