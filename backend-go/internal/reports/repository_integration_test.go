package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func reportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("database must be localhost and end with _test")
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

func TestSaveReportKeepsRevisionAndReceiptCompletionAtomic(t *testing.T) {
	db := reportTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	key := fmt.Sprintf("report-repo-test-%d", time.Now().UnixNano())
	receiptKey := "report-repo-test:" + key
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM report_revisions WHERE report_id IN (SELECT id FROM reports WHERE key = ?)", key).Error
		_ = db.Exec("DELETE FROM reports WHERE key = ?", key).Error
		_ = db.Exec("DELETE FROM receipts WHERE logical_key = ?", receiptKey).Error
	})
	start := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	first := map[string]any{"metrics": map[string]int{"totalEvents": 2}, "leadItemId": "fixture"}
	written, err := repo.SaveReport(ctx, "daily", key, start, end, first, nil, nil, nil)
	if err != nil || !written {
		t.Fatalf("initial report write = %t, %v", written, err)
	}
	saved, err := repo.SavedReport(ctx, "daily", key)
	if err != nil || saved == nil || saved.Entries != 2 {
		t.Fatalf("saved report = %#v, %v", saved, err)
	}
	var receiptID int64
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,subject,status,request,attempts,origin)
 VALUES(?, 'fixture', 'report_weekly', 'report:weekly:fixture', 'received', '{}'::jsonb, 1, 'live') RETURNING id`, receiptKey).Scan(&receiptID).Error; err != nil {
		t.Fatal(err)
	}
	reason := "integration fixture"
	model := "fixture-model"
	second := map[string]any{"metrics": map[string]int{"totalEvents": 4}, "leadItemId": "fixture-2"}
	written, err = repo.SaveReport(ctx, "daily", key, start, end.Add(time.Hour), second, &reason, &model, []int64{receiptID})
	if err != nil || !written {
		t.Fatalf("report regeneration = %t, %v", written, err)
	}
	var stored struct {
		Revision int             `gorm:"column:revision"`
		Content  json.RawMessage `gorm:"column:content"`
		Model    *string         `gorm:"column:model"`
	}
	if err := db.Raw("SELECT revision, content, model FROM reports WHERE kind = 'daily' AND key = ?", key).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Revision != 2 || stored.Model == nil || *stored.Model != model {
		t.Fatalf("regenerated report metadata = %#v", stored)
	}
	var revisions int
	if err := db.Raw("SELECT count(*)::int FROM report_revisions WHERE report_id = (SELECT id FROM reports WHERE key = ?)", key).Scan(&revisions).Error; err != nil || revisions != 1 {
		t.Fatalf("revision archive count = %d, %v", revisions, err)
	}
	var receiptStatus string
	if err := db.Raw("SELECT status FROM receipts WHERE id = ?", receiptID).Scan(&receiptStatus).Error; err != nil || receiptStatus != "completed" {
		t.Fatalf("completed model receipt = %q, %v", receiptStatus, err)
	}
}
