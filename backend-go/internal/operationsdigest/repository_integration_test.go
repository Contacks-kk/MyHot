package operationsdigest

import (
	"context"
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

func localOperationsDigestTestORM(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end with _test or _ci")
	}
	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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

func TestGormRepositoryRecognizesOneDigestAttemptInBeijingDay(t *testing.T) {
	db := localOperationsDigestTestORM(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(3 * 24 * time.Hour).Truncate(time.Second)
	end := start.Add(24 * time.Hour)
	repository := NewRepository(db)
	if found, err := repository.HasRun(ctx, start, end); err != nil || found {
		t.Fatalf("future test range should be empty: found=%t err=%v", found, err)
	}
	run := model.JobRun{Job: "ops.digest", StartedAt: start.Add(time.Hour), FinishedAt: timePtr(start.Add(time.Hour)), Status: "ok"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&run).Error })
	if found, err := repository.HasRun(ctx, start, end); err != nil || !found {
		t.Fatalf("digest attempt should suppress another run that day: found=%t err=%v", found, err)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
