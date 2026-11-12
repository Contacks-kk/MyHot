package publiccontent

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

func TestHotTopicsRepositoryRechecksTheSavedRepresentativeAgainstCurrentPublication(t *testing.T) {
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

	prefix := fmt.Sprintf("v1hot_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	factID := prefix + "_fact"
	ruleVersion := prefix + "_ranking"
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM hot_rankings WHERE rule_version = ?", ruleVersion).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM facts WHERE public_id = ?", factID).Error
		_ = orm.Exec("DELETE FROM stories WHERE public_id = ?", publicID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := orm.Exec("INSERT INTO sources(id,name,kind) VALUES (?, 'Fixture source（channel）', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO stories(public_id,title) VALUES (?, 'Fixture story')", publicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO facts(public_id,story_id,title) VALUES (?, (SELECT id FROM stories WHERE public_id = ?), 'Fixture fact')", factID, publicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, 'https://source.example/fixture', 'Fixture article', ?, ?)`, articleID, sourceID, prefix+"_identity", now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,url,channel,discovered_at,timeline_at,visibility,eligible,selected)
		VALUES (?, ?, 'Fixture article', 'https://source.example/fixture', 'news', ?, ?, 'public', true, false)`, articleID, sourceID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO fact_articles(fact_id,article_id,role) VALUES ((SELECT id FROM facts WHERE public_id = ?), ?, 'primary')`, factID, articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO hot_rankings(computed_at,rule_version,entries,evidence,published)
		VALUES (now() + interval '1 hour', ?, jsonb_build_array(jsonb_build_object(
		'rank', 1,
		'storyId', (SELECT id FROM stories WHERE public_id = ?),
		'storyPublicId', ?, 'representativeItemId', ?, 'sourceCount', 2, 'signalCount', 1,
		'participantCount', 3, 'sourceNames', jsonb_build_array('Fixture source（channel）'))), '{}', true)`, ruleVersion, publicID, publicID, articleID).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := NewRepository(orm).HotTopics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RepresentativeSource == nil || *rows[0].RepresentativeSource != "Fixture source（channel）" || rows[0].RepresentativeURL != "https://source.example/fixture" || rows[0].StoryTitle != "Fixture story" {
		t.Fatalf("current hot topics = %#v", rows)
	}
	siteRanking, err := NewRepository(orm).SiteHotRanking(context.Background(), now)
	if err != nil || siteRanking == nil || len(siteRanking.Entries) != 1 || siteRanking.Entries[0].StoryPublicID != publicID || siteRanking.Entries[0].RepresentativeURL != "https://source.example/fixture" {
		t.Fatalf("current site hot ranking = %#v, err=%v", siteRanking, err)
	}
	details, err := NewRepository(orm).SiteHotDetails(context.Background(), siteRanking.Entries, siteRanking.ComputedAt, now)
	if err != nil || details.Texts[siteRanking.Entries[0].StoryID].Latest == nil || *details.Texts[siteRanking.Entries[0].StoryID].Latest != "Fixture article" {
		t.Fatalf("site hot details = %#v, err=%v", details, err)
	}
	if err := orm.Exec("UPDATE publications SET visibility = 'withdrawn' WHERE article_id = ?", articleID).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = NewRepository(orm).HotTopics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("withdrawn saved representative remained visible: %#v", rows)
	}
	siteRanking, err = NewRepository(orm).SiteHotRanking(context.Background(), now)
	if err != nil || siteRanking == nil || len(siteRanking.Entries) != 0 {
		t.Fatalf("withdrawn site hot representative remained visible: %#v, err=%v", siteRanking, err)
	}
}
