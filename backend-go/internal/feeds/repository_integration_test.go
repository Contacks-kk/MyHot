package feeds

import (
	"context"
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

func TestFullFeedDropsBodyWhenRedistributionPermissionIsRevoked(t *testing.T) {
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

	prefix := fmt.Sprintf("feed_license_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec(`INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES (?, 'Licensed fixture', 'rss', 'T1', 'editorial')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,body_text,body_html,body_status,discovered_at,timeline_at,published_at)
		VALUES (?, ?, ?, ?, 'Licensed title', 'Licensed full text', '<p>LICENSED-BODY</p>', 'ok', ?, ?, ?)`, articleID, sourceID, articleID, "https://source.example/licensed", now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,summary,url,channel,visibility,eligible,selected,seat,visible_after,discovered_at,timeline_at,published_at,syndicate)
		VALUES (?, ?, 'Licensed title', 'Licensed summary', 'https://source.example/licensed', 'news', 'public', true, true, true, ?, ?, ?, ?, true)`, articleID, sourceID, now.Add(-time.Minute), now, now, now).Error; err != nil {
		t.Fatal(err)
	}

	repository := NewGormRepository(orm)
	service := NewService(repository, nil, Config{SiteURL: "https://myhot.example", SiteName: "Example"})
	before, err := service.ItemFeed(context.Background(), "selected-full", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "<content:encoded><![CDATA[<p>LICENSED-BODY</p>") {
		t.Fatalf("redistributable source body missing before revocation: %s", before)
	}
	if err := orm.Exec("UPDATE publications SET syndicate = false WHERE article_id = ?", articleID).Error; err != nil {
		t.Fatal(err)
	}
	after, err := service.ItemFeed(context.Background(), "selected-full", "", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after, "Licensed summary") || strings.Contains(after, "LICENSED-BODY") || strings.Contains(after, "<content:encoded>") {
		t.Fatalf("revoked redistribution must retain the RSS item summary and remove its body: %s", after)
	}
}
