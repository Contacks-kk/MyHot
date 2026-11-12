package retention

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func localTestORM(t *testing.T) *gorm.DB {
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

func TestGormRepositoryRetentionCleanupAndDailyRunLock(t *testing.T) {
	db := localTestORM(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	prefix := fmt.Sprintf("retention_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM delivery_leases WHERE holder = ?", prefix).Error
		_ = db.Exec("DELETE FROM job_runs WHERE job LIKE ?", prefix+"_%").Error
	})
	leaseKeys := []string{prefix + "_expired", prefix + "_boundary", prefix + "_future"}
	for i, expiry := range []time.Time{now.Add(-time.Second), now, now.Add(time.Hour)} {
		if err := db.Exec("INSERT INTO delivery_leases(lease_key, holder, expires_at) VALUES (?, ?, ?)", leaseKeys[i], prefix, expiry).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		name string
		age  time.Duration
	}{
		{name: "ok-old", age: 31 * 24 * time.Hour},
		{name: "failed-89d", age: 89 * 24 * time.Hour},
		{name: "failed-91d", age: 91 * 24 * time.Hour},
		{name: "ok-recent", age: 29 * 24 * time.Hour},
	} {
		status := "ok"
		if strings.HasPrefix(fixture.name, "failed") {
			status = "failed"
		}
		var id int64
		if err := db.Raw("INSERT INTO job_runs(job, status, started_at) VALUES (?, ?, ?) RETURNING id", prefix+"_"+fixture.name, status, now.Add(-fixture.age)).Scan(&id).Error; err != nil {
			t.Fatal(err)
		}
	}
	start, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	day := now.In(start)
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, start)
	repository := NewRepository(db)
	runID, created, err := repository.BeginRun(ctx, prefix+"_daily", dayStart, dayStart.AddDate(0, 0, 1))
	if err != nil || !created || runID == 0 {
		t.Fatalf("first daily run: id=%d created=%v err=%v", runID, created, err)
	}
	if hasRun, err := repository.HasRun(ctx, prefix+"_daily", dayStart, dayStart.AddDate(0, 0, 1)); err != nil || !hasRun {
		t.Fatalf("new daily run is not visible in its Beijing day: hasRun=%v err=%v", hasRun, err)
	}
	if err := repository.FailRun(ctx, runID, "transient fixture failure"); err != nil {
		t.Fatal(err)
	}
	retryID, created, err := repository.BeginRun(ctx, prefix+"_daily", dayStart, dayStart.AddDate(0, 0, 1))
	if err != nil || !created || retryID == 0 {
		t.Fatalf("retry run: id=%d created=%v err=%v", retryID, created, err)
	}
	if duplicateID, created, err := repository.BeginRun(ctx, prefix+"_daily", dayStart, dayStart.AddDate(0, 0, 1)); err != nil || created || duplicateID != 0 {
		t.Fatalf("duplicate daily run: id=%d created=%v err=%v", duplicateID, created, err)
	}
	leases, runs, err := repository.CleanupDatabase(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if leases != 1 || runs != 2 {
		t.Fatalf("deleted leases=%d runs=%d, want 1 and 2", leases, runs)
	}
	var remainingLeases int64
	if err := db.Table("delivery_leases").Where("holder = ?", prefix).Count(&remainingLeases).Error; err != nil || remainingLeases != 2 {
		t.Fatalf("remaining leases=%d err=%v", remainingLeases, err)
	}
	var remainingRuns int64
	if err := db.Model(&model.JobRun{}).Where("job LIKE ?", prefix+"_%").Count(&remainingRuns).Error; err != nil || remainingRuns != 4 {
		t.Fatalf("remaining job runs=%d err=%v", remainingRuns, err)
	}
}
