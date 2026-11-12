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

func TestV1StoriesRepositoryResolvesAliasesAndFiltersPublicReports(t *testing.T) {
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

	prefix := fmt.Sprintf("zzv1story_%d", time.Now().UnixNano())
	sourceID, articleID, factID := prefix+"_source", prefix+"_article", prefix+"_fact"
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	aliasID := fmt.Sprintf("00000000-0000-4000-8000-%012x", (uint64(time.Now().UnixNano())+1)&0xffffffffffff)
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM story_aliases WHERE public_id = ?::uuid", aliasID).Error
		_ = orm.Exec("DELETE FROM stories WHERE public_id = ?::uuid", publicID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources(id,name,kind,participation_mode) VALUES (?, 'Story fixture source', 'external', 'editorial')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO stories(public_id,title,digest,digest_updated_at) VALUES (?::uuid, 'Fixture story', NULL, NULL)", publicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, ?, 'Fixture report', ?, ?)`, articleID, sourceID, articleID, "https://fixture.example/story", now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,summary,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,selected)
		VALUES (?, ?, 'Fixture report', 'Fixture summary', 'https://fixture.example/story', 'news', ?, ?, ?, 'public', true, false)`, articleID, sourceID, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO facts(public_id,story_id,title,subject,action) VALUES (?, (SELECT id FROM stories WHERE public_id = ?::uuid), 'Fixture fact', 'Fixture subject', 'Fixture action')", factID, publicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO fact_articles(fact_id,article_id,role) VALUES ((SELECT id FROM facts WHERE public_id = ?), ?, 'primary')", factID, articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO story_aliases(public_id,story_id) VALUES (?::uuid, (SELECT id FROM stories WHERE public_id = ?::uuid))", aliasID, publicID).Error; err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(orm)
	lookup, err := repository.ResolveV1Story(context.Background(), publicID)
	if err != nil || lookup.Kind != "found" {
		t.Fatalf("current story lookup = %#v, err=%v", lookup, err)
	}
	alias, err := repository.ResolveV1Story(context.Background(), aliasID)
	if err != nil || alias.Kind != "merged" || alias.Target != publicID {
		t.Fatalf("alias lookup = %#v, err=%v", alias, err)
	}
	if missing, err := repository.ResolveV1Story(context.Background(), "not-a-uuid"); err != nil || missing.Kind != "not_found" {
		t.Fatalf("invalid story lookup = %#v, err=%v", missing, err)
	}
	response, found, err := repository.LoadV1Story(context.Background(), lookup.ID, now)
	if err != nil || !found || response.Story.PublicID != publicID || response.Story.ReportCount != 1 || response.Story.SourceCount != 1 || len(response.Story.Reports) != 1 {
		t.Fatalf("story response = %#v found=%v err=%v", response, found, err)
	}
	if response.Story.Reports[0].ID != articleID || response.Story.Reports[0].Summary == nil || *response.Story.Reports[0].Summary != "Fixture summary" || response.Story.Digest != nil {
		t.Fatalf("story report = %#v", response.Story.Reports[0])
	}
	if err := orm.Exec("UPDATE publications SET visibility = 'withdrawn' WHERE article_id = ?", articleID).Error; err != nil {
		t.Fatal(err)
	}
	response, found, err = repository.LoadV1Story(context.Background(), lookup.ID, now)
	if err != nil || found || response.Story.PublicID != "" {
		t.Fatalf("withdrawn-only story should be private-missing: %#v found=%v err=%v", response, found, err)
	}
}
