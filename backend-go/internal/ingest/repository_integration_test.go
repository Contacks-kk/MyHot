package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/materials"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

/** TestGormRepositoryIngestsIntoExistingSchema preserves normalized ingest and repeat-request deduplication. */
func TestGormRepositoryIngestsIntoExistingSchema(t *testing.T) {
	dsn := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("integration database must be localhost and end with _test")
	}
	orm, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx := context.Background()
	sourceID := fmt.Sprintf("ingest_test_%d", time.Now().UnixNano())
	service := NewService(NewRepository(orm), materials.NewService(materials.NewRepository(orm)))
	input := json.RawMessage(fmt.Sprintf(`{"sourceId":%q,"sourceName":"External fixture","items":[
		{"title":"Pushed article","url":%q,"raw":{"_aihot":{"baseline":true},"fixture":7}},
		{"title":"duplicate","url":%q}
	]}`, sourceID, "http://www.example.invalid/story/"+sourceID+"/?utm_source=test", "https://example.invalid/story/"+sourceID))
	result, err := service.Ingest(ctx, input)
	if err != nil || !result.OK || result.Created != 1 {
		t.Fatalf("ingest = %#v: %v", result, err)
	}
	again, err := service.Ingest(ctx, input)
	if err != nil || !again.OK || again.Created != 0 {
		t.Fatalf("repeated ingest = %#v: %v", again, err)
	}
	var article struct {
		SourceID       string          `gorm:"column:source_id"`
		IdentityKey    string          `gorm:"column:identity_key"`
		Title          string          `gorm:"column:title"`
		Backfill       bool            `gorm:"column:backfill"`
		BackfillReason string          `gorm:"column:backfill_reason"`
		Raw            json.RawMessage `gorm:"column:raw"`
	}
	if err := orm.Raw("SELECT source_id, identity_key, title, backfill, backfill_reason, raw FROM articles WHERE source_id = ?", sourceID).Scan(&article).Error; err != nil {
		t.Fatal(err)
	}
	if article.SourceID != sourceID || article.IdentityKey != "url:https://example.invalid/story/"+sourceID || article.Title != "Pushed article" || !article.Backfill || article.BackfillReason != "reported-baseline" || !strings.Contains(string(article.Raw), `"fixture": 7`) {
		t.Fatalf("stored article changed: %#v raw=%s", article, article.Raw)
	}
	var articleCount int64
	if err := orm.Table("articles").Where("source_id = ?", sourceID).Count(&articleCount).Error; err != nil || articleCount != 1 {
		t.Fatalf("repeated ingest created %d article rows: %v", articleCount, err)
	}
	var source struct {
		Kind              string `gorm:"column:kind"`
		ParticipationMode string `gorm:"column:participation_mode"`
		Enabled           bool   `gorm:"column:enabled"`
	}
	if err := orm.Raw("SELECT kind, participation_mode, enabled FROM sources WHERE id = ?", sourceID).Scan(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.Kind != "external" || source.ParticipationMode != "isolated" || !source.Enabled {
		t.Fatalf("auto-created source changed: %#v", source)
	}
	var events []struct {
		Client  string `gorm:"column:client"`
		Created string `gorm:"column:created"`
	}
	if err := orm.Raw("SELECT client, summary->>'created' AS created FROM ingest_events WHERE client = 'ingest-items' AND summary->>'sourceId' = ? ORDER BY id", sourceID).Scan(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Client != "ingest-items" || events[0].Created != "1" || events[1].Client != "ingest-items" || events[1].Created != "0" {
		t.Fatalf("ingest audit changed across initial and repeated requests: %#v", events)
	}
}

func TestGormRepositoryRejectsDisabledSourceWithoutRecordingSuccess(t *testing.T) {
	dsn := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("integration database must be localhost and end with _test")
	}
	orm, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx := context.Background()
	sourceID := fmt.Sprintf("ingest_paused_test_%d", time.Now().UnixNano())
	lastOKAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := orm.Exec(`INSERT INTO sources (id, name, kind, participation_mode, enabled, health, last_ok_at)
		VALUES (?, 'Paused ingest fixture', 'external', 'isolated', false, 'paused', ?)`, sourceID, lastOKAt).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(orm), materials.NewService(materials.NewRepository(orm)))
	input := json.RawMessage(fmt.Sprintf(`{"sourceId":%q,"items":[{"title":"New report","url":"https://example.invalid/%s"}]}`, sourceID, sourceID))
	if _, err := service.Ingest(ctx, input); !IsPaused(err) {
		t.Fatalf("disabled source error = %v", err)
	}
	var source struct {
		LastOKAt *time.Time `gorm:"column:last_ok_at"`
	}
	if err := orm.Raw("SELECT last_ok_at FROM sources WHERE id = ?", sourceID).Scan(&source).Error; err != nil {
		t.Fatal(err)
	}
	var articles int64
	if err := orm.Table("articles").Where("source_id = ?", sourceID).Count(&articles).Error; err != nil {
		t.Fatal(err)
	}
	if source.LastOKAt == nil || !source.LastOKAt.Equal(lastOKAt) || articles != 0 {
		t.Fatalf("paused source changed: last_ok_at=%v articles=%d", source.LastOKAt, articles)
	}
}
