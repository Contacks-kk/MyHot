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

func TestReadSitemapUsesCurrentPublicScopeAndIndexability(t *testing.T) {
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

	prefix := fmt.Sprintf("sitemap_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	storyID := fmt.Sprintf("%08x-1234-4234-8234-%012x", uint32(time.Now().Unix()), uint64(time.Now().UnixNano())&0xffffffffffff)
	reportKey := "2999-12-31"
	articleIDs := make([]string, 0, 21)
	for i := 0; i < 20; i++ {
		articleIDs = append(articleIDs, fmt.Sprintf("%s_selected_%02d", prefix, i))
	}
	indexableID := prefix + "_indexable"
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM reports WHERE kind = 'daily' AND key = ?", reportKey).Error
		_ = orm.Exec("DELETE FROM articles WHERE id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM facts WHERE public_id LIKE ?", prefix+"_%").Error
		_ = orm.Exec("DELETE FROM stories WHERE public_id = ?", storyID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources(id,name,kind) VALUES (?, 'Sitemap fixture', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	released := now.Add(-time.Hour)
	for index, id := range articleIDs {
		timeline := released.Add(-time.Duration(index) * time.Minute)
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sourceID, id, "https://fixture.example/"+id, "Sitemap company report", timeline, timeline).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,summary,url,channel,discovered_at,timeline_at,sort_at,visible_after,visibility,eligible,selected,seat,tags)
			VALUES (?, ?, 'Sitemap company report', 'Summary', ?, 'news', ?, ?, ?, ?, 'public', true, true, true, ARRAY['entity:fixture']::text[])`, id, sourceID, "https://fixture.example/"+id, timeline, timeline, timeline, released).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, ?, 'Indexable item', ?, ?)`, indexableID, sourceID, indexableID, "https://fixture.example/"+indexableID, released, released).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,summary,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,selected,indexable,tags,updated_at)
		VALUES (?, ?, 'Indexable item', 'Summary', ?, 'news', ?, ?, ?, 'public', true, false, true, ARRAY['entity:fixture']::text[], ?)`, indexableID, sourceID, "https://fixture.example/"+indexableID, released, released, released, released).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO stories(public_id,title,latest_at) VALUES (?, 'Sitemap story', ?)", storyID, released).Error; err != nil {
		t.Fatal(err)
	}
	var storyPK int64
	if err := orm.Raw("SELECT id FROM stories WHERE public_id = ?", storyID).Scan(&storyPK).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO facts(public_id,story_id,title) VALUES (?, ?, 'Sitemap fact')", prefix+"_fact", storyPK).Error; err != nil {
		t.Fatal(err)
	}
	var factPK int64
	if err := orm.Raw("SELECT id FROM facts WHERE public_id = ?", prefix+"_fact").Scan(&factPK).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO fact_articles(fact_id,article_id,role) VALUES (?, ?, 'report')", factPK, articleIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO reports(kind,key,window_start,window_end,content,generated_at,origin)
		VALUES ('daily', ?, ?, ?, '{}'::jsonb, ?, 'manual')`, reportKey, now.Add(-24*time.Hour), now, now).Error; err != nil {
		t.Fatal(err)
	}

	data, err := NewRepository(orm).ReadSitemap(context.Background(), now, []topics.Definition{{Slug: "fixture-topic", Tags: []string{"entity:fixture"}}}, SitemapMaxURLs)
	if err != nil {
		t.Fatal(err)
	}
	if data.LatestSelectedAt == nil || !data.LatestSelectedAt.Equal(released) || data.LatestDailyAt == nil || !data.LatestDailyAt.Equal(now) {
		t.Fatalf("sitemap latest timestamps = selected:%v daily:%v", data.LatestSelectedAt, data.LatestDailyAt)
	}
	if len(data.Reports) != 1 || data.Reports[0].Kind != "daily" || data.Reports[0].Key != reportKey {
		t.Fatalf("sitemap report entries = %#v", data.Reports)
	}
	if len(data.Topics) != 1 || !data.Topics[0].Indexable || data.Topics[0].Pages != 1 || data.Topics[0].ChangedAt == nil || !data.Topics[0].ChangedAt.Equal(released) {
		t.Fatalf("sitemap topic entries = %#v", data.Topics)
	}
	if len(data.Stories) != 1 || data.Stories[0].PublicID != storyID {
		t.Fatalf("sitemap story entries = %#v", data.Stories)
	}
	if len(data.Items) != 1 || data.Items[0].ID != indexableID || !data.Items[0].UpdatedAt.Equal(released) {
		t.Fatalf("sitemap indexable items = %#v", data.Items)
	}
}
