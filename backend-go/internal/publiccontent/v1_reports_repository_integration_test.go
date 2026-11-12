package publiccontent

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
	"gorm.io/gorm/logger"
)

func TestV1ReportsRepositoryAppliesCurrentPublicationScopeAndProjectsLeadReplacement(t *testing.T) {
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

	prefix := fmt.Sprintf("zzv1report_%d", time.Now().UnixNano())
	sourceID, withdrawnID, currentID := prefix+"_source", prefix+"_withdrawn", prefix+"_current"
	const reportKey = "2999-12-31"
	generated := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM reports WHERE content->>'fixtureTag' = ?", prefix).Error
		_ = orm.Exec("DELETE FROM articles WHERE id IN ?", []string{withdrawnID, currentID}).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources(id,name,kind) VALUES (?, 'Report fixture source', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{withdrawnID, currentID} {
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sourceID, id, "https://fixture.example/"+id, "Fixture "+id, generated, generated).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,summary,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,selected)
		VALUES (?, ?, 'Withdrawn lead', 'Withdrawn summary', 'https://fixture.example/withdrawn', 'news', ?, ?, ?, 'public', true, false),
		       (?, ?, 'Current replacement', 'Current fallback summary', 'https://fixture.example/current', 'news', ?, ?, ?, 'public', true, false)`,
		withdrawnID, sourceID, generated, generated, generated, currentID, sourceID, generated, generated, generated).Error; err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(map[string]any{
		"fixtureTag": prefix, "leadItemId": withdrawnID, "highlights": []string{currentID},
		"lead": map[string]any{"title": "Withdrawn lead", "leadParagraph": "Original prose"},
		"sections": []any{map[string]any{"label": "Fixture", "items": []any{
			map[string]any{"itemId": withdrawnID, "title": "Withdrawn lead", "summary": "Withdrawn summary"},
			map[string]any{"itemId": currentID, "title": "Current replacement"},
		}}}, "flashes": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO reports(kind,key,window_start,window_end,content,generated_at,origin)
		VALUES ('daily', ?, ?, ?, ?::jsonb, ?, 'manual')`, reportKey, generated.Add(-24*time.Hour), generated, string(content), generated).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(NewRepository(orm))
	before, err := service.V1Dailies(context.Background(), 30, generated.Add(time.Hour), "https://myhot.example", "Example")
	if err != nil || before.Count == 0 || before.Items[0]["leadTitle"].(*string) == nil || *before.Items[0]["leadTitle"].(*string) != "Withdrawn lead" {
		t.Fatalf("published report index = %#v, err=%v", before, err)
	}
	if err := orm.Exec("UPDATE publications SET visibility = 'withdrawn' WHERE article_id = ?", withdrawnID).Error; err != nil {
		t.Fatal(err)
	}
	after, err := service.V1Dailies(context.Background(), 30, generated.Add(2*time.Hour), "https://myhot.example", "Example")
	if err != nil || after.Count == 0 || after.Items[0]["leadTitle"].(*string) == nil || *after.Items[0]["leadTitle"].(*string) != "Current replacement" {
		t.Fatalf("withdrawn report index = %#v, err=%v", after, err)
	}
	if paragraph := after.Items[0]["leadParagraph"].(*string); paragraph == nil || *paragraph != "Current fallback summary" {
		t.Fatalf("replacement paragraph = %#v", paragraph)
	}
	detail, err := service.V1Daily(context.Background(), reportKey, false, generated.Add(2*time.Hour), "https://myhot.example", "Example")
	if err != nil || detail == nil || detail.Report.Lead == nil || detail.Report.Lead.Title != "Current replacement" || detail.Report.Lead.LeadParagraph != "Current fallback summary" || len(detail.Report.Sections[0].Items) != 1 {
		t.Fatalf("withdrawn report detail = %#v, err=%v", detail, err)
	}
}
