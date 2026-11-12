package publiccontent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/topics"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSiteItemRepositoryLoadsLicensedDetailAndCurrentGroup(t *testing.T) {
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

	prefix := fmt.Sprintf("siteitem_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	var factID int64
	cleanup := func() {
		if factID != 0 {
			_ = orm.Exec("DELETE FROM facts WHERE id = ?", factID).Error
		}
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	}
	t.Cleanup(cleanup)
	if err := orm.Exec("INSERT INTO sources (id,name,kind) VALUES (?, 'Fixture source', 'rss')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,author,language,discovered_at,timeline_at,body_text,body_html,body_status)
		VALUES (?, ?, ?, ?, 'Original title', 'Writer', 'en', ?, ?, 'English body', '<h2>English body</h2>', 'ok')`, articleID, sourceID, prefix, "https://fixture.example/article", now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications (article_id,source_id,title,original_title,summary,reason,category,tags,score,channel,url,published_at,discovered_at,timeline_at,visibility,eligible,selected,seat,visible_after,body_mode,indexable)
		VALUES (?, ?, 'Chinese title', 'Original title', 'Summary', 'Reason', 'ai-models', ARRAY['Agent','entity:openai'], 81.6, 'news', ?, ?, ?, ?, 'public', true, true, true, ?, 'full', true)`, articleID, sourceID, "https://fixture.example/article", now, now, now, now.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO translations (article_id,lang,revision,body_html,body_text,complete) VALUES (?, 'zh', 1, '<h2>Chinese body</h2>', 'Chinese body', false)", articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Raw("INSERT INTO facts (public_id,title) VALUES (?, 'Fixture fact') RETURNING id", prefix+"_fact").Row().Scan(&factID); err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?, ?, 'primary')", factID, articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("UPDATE publications SET fact_id = ? WHERE article_id = ?", factID, articleID).Error; err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(orm)
	row, err := repository.SiteItem(context.Background(), articleID, []topics.Definition{{Slug: "openai", Name: "OpenAI", Tags: []string{"entity:openai"}, Position: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.Item.Title != "Chinese title" || row.Item.Selected == nil || !*row.Item.Selected || row.SourceMode != "editorial" || row.TranslationText == nil || *row.TranslationText != "Chinese body" || len(row.TopicSlugs) != 1 || row.TopicSlugs[0] != "openai" {
		t.Fatalf("site item repository projection = %#v", row)
	}
	group, err := repository.SiteItemGroup(context.Background(), factID, now)
	if err != nil || group == nil || group.FactID != prefix+"_fact" || group.ReportCount != 1 || group.AdditionalSourceCount != 0 {
		t.Fatalf("item group = %#v, %v", group, err)
	}
	holder, err := repository.SiteItemSeatHolder(context.Background(), factID, now)
	if err != nil || holder == nil || holder.ID != articleID {
		t.Fatalf("seat holder = %#v, %v", holder, err)
	}
}
