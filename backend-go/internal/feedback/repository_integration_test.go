package feedback

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIntegrationRepository(t *testing.T) (*gorm.DB, *GormRepository) {
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

func TestFeedbackUpdateRollsBackWhenAuditInsertFails(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("feedback_tx_%d", time.Now().UnixNano())
	row := model.Feedback{Content: "transaction rollback fixture", SourceHash: fixture, Status: "new"}
	if err := orm.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	constraint := fmt.Sprintf("feedback_audit_fail_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = orm.Exec(`ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS ` + constraint).Error
		_ = orm.Where("id = ?", row.ID).Delete(&model.Feedback{}).Error
		_ = orm.Where("subject = ?", "feedback:"+strconv.FormatInt(row.ID, 10)).Delete(&model.AdminAuditLog{}).Error
	})
	if err := orm.Exec(`ALTER TABLE audit_log ADD CONSTRAINT ` + constraint + ` CHECK (action <> 'feedback.update') NOT VALID`).Error; err != nil {
		t.Fatal(err)
	}
	patch := Patch{Status: stringPointer("triaged"), Version: row.UpdatedAt.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")}
	_, _, err := repository.Update(ctx, row.ID, patch, "admin:transaction-test")
	if err == nil {
		t.Fatal("audit insert failure unexpectedly committed a feedback update")
	}
	var after model.Feedback
	if err := orm.Select("status").First(&after, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "new" {
		t.Fatalf("feedback change survived audit failure: status=%q", after.Status)
	}
	var auditCount int64
	if err := orm.Model(&model.AdminAuditLog{}).Where("subject = ?", "feedback:"+strconv.FormatInt(row.ID, 10)).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 0 {
		t.Fatalf("audit row unexpectedly committed: count=%d", auditCount)
	}
}

func TestFeedbackSubmissionBanAndForwardRetryRepository(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("feedback_submit_%d", time.Now().UnixNano())
	rowID, err := repository.Create(ctx, Submission{Content: "forward fixture", SourceHash: fixture, ScreenshotKey: stringPointer("local:fixture.png")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Where("id = ?", rowID).Delete(&model.Feedback{}).Error
		_ = orm.Where("source_hash = ?", fixture).Delete(&model.FeedbackBan{}).Error
	})
	if banned, err := repository.Banned(ctx, []string{fixture}); err != nil || banned {
		t.Fatalf("new feedback source unexpectedly banned: banned=%t err=%v", banned, err)
	}
	if err := orm.Create(&model.FeedbackBan{SourceHash: fixture}).Error; err != nil {
		t.Fatal(err)
	}
	if banned, err := repository.Banned(ctx, []string{fixture}); err != nil || !banned {
		t.Fatalf("stored feedback ban was ignored: banned=%t err=%v", banned, err)
	}
	now := time.Now().UTC()
	if err := orm.Model(&model.Feedback{}).Where("id = ?", rowID).Update("created_at", now.Add(-6*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	ids, err := repository.PendingForwardIDs(ctx, now.Add(-5*time.Minute), now.Add(-7*24*time.Hour), 20)
	if err != nil || len(ids) != 1 || ids[0] != rowID {
		t.Fatalf("pending feedback scan missed the retry: ids=%v err=%v", ids, err)
	}
	if err := repository.SetScreenshotKey(ctx, rowID, "feishu:image-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetForwardError(ctx, rowID, "transient send error"); err != nil {
		t.Fatal(err)
	}
	forward, err := repository.ForwardRecord(ctx, rowID)
	if err != nil || forward == nil || forward.ScreenshotKey == nil || *forward.ScreenshotKey != "feishu:image-fixture" {
		t.Fatalf("feedback forwarding record was not loaded: %#v err=%v", forward, err)
	}
	if err := repository.MarkForwarded(ctx, rowID, now); err != nil {
		t.Fatal(err)
	}
	if ids, err = repository.PendingForwardIDs(ctx, now.Add(time.Minute), now.Add(-7*24*time.Hour), 20); err != nil || len(ids) != 0 {
		t.Fatalf("completed feedback stayed in retry scan: ids=%v err=%v", ids, err)
	}
}

func stringPointer(value string) *string { return &value }
