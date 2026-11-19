package admincontent

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
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

func TestSearchByIDURLAndTitleUsesPublicationTitle(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_%d", time.Now().UnixNano())
	sourceID := "source_" + fixture
	articleID := "article_" + fixture
	articleURL := "https://example.invalid/news/" + fixture + "?b=2"
	identity := "url:" + articleURL
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources (id,name,kind) VALUES (?, 'Content search fixture', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, ?, 'Original title', now(), now())`, articleID, sourceID, identity, articleURL).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications (article_id,title,source_id,channel,url,discovered_at,timeline_at,sort_at)
		VALUES (?, 'Published title', ?, 'news', ?, now(), now(), now())`, articleID, sourceID, articleURL).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO article_discoveries (article_id,source_id,via) VALUES (?,?,'fixture')`, articleID, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO article_revisions (article_id,revision,title,content_hash) VALUES (?,1,'Original title','fixture-hash')`, articleID).Error; err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		query string
		field string
	}{
		{query: articleID, field: "id"},
		{query: "http://www.example.invalid/news/" + fixture + "?utm_source=search&b=2#section", field: "url"},
		{query: "Published title", field: "title"},
	}
	for _, check := range checks {
		rows, err := NewService(repository).Search(ctx, check.query)
		if err != nil || len(rows) != 1 || rows[0].ID != articleID || rows[0].Title != "Published title" {
			t.Fatalf("%s search contract changed: query=%q rows=%#v err=%v", check.field, check.query, rows, err)
		}
	}
	chain, err := repository.Chain(ctx, articleID)
	if err != nil || chain == nil || chain.Article.ID != articleID || chain.Article.SourceName != "Content search fixture" || len(chain.Discoveries) != 1 || len(chain.Revisions) != 1 || chain.Publication == nil || chain.Publication.Title != "Published title" || chain.Analyses == nil || chain.Deliveries == nil || chain.History == nil {
		t.Fatalf("content chain contract changed: chain=%#v err=%v", chain, err)
	}
}

func TestSetSEOUpdatesIndexProjectionAndAuditsAtomically(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_seo_%d", time.Now().UnixNano())
	sourceID, articleID := "source_"+fixture, "article_"+fixture
	articleURL := "https://example.invalid/" + fixture
	actor := "admincontent-seo-" + fixture
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM audit_log WHERE actor = ?", actor).Error
		_ = orm.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources (id,name,kind) VALUES (?, 'SEO fixture', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, ?, 'SEO fixture', now(), now())`, articleID, sourceID, "url:"+articleURL, articleURL).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications (article_id,title,summary,source_id,channel,url,discovered_at,timeline_at,sort_at)
		VALUES (?, 'SEO fixture', 'readable summary', ?, 'news', ?, now(), now(), now())`, articleID, sourceID, articleURL).Error; err != nil {
		t.Fatal(err)
	}

	result, err := repository.SetSEO(ctx, articleID, SEOInput{Indexed: true, Reason: "reviewed"}, actor)
	if err != nil || result == nil || !result.Changed || result.ArticleID != articleID || result.Visibility != "public" {
		t.Fatalf("index request result=%#v err=%v", result, err)
	}
	var indexed, excluded sql.NullTime
	var visible bool
	if err := orm.Raw("SELECT seo_indexed_at, seo_excluded_at, indexable FROM publications WHERE article_id = ?", articleID).Row().Scan(&indexed, &excluded, &visible); err != nil {
		t.Fatal(err)
	}
	if !indexed.Valid || excluded.Valid || !visible {
		t.Fatalf("index decision indexed=%v excluded=%v indexable=%t", indexed, excluded, visible)
	}
	var auditCount int64
	if err := orm.Model(&model.AdminAuditLog{}).Where("actor = ? AND action = ? AND subject = ?", actor, "content.seo", "content:"+articleID).Count(&auditCount).Error; err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}

	result, err = repository.SetSEO(ctx, articleID, SEOInput{Indexed: false, Reason: "withdrawn from search"}, actor)
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("exclude request result=%#v err=%v", result, err)
	}
	if err := orm.Raw("SELECT seo_indexed_at, seo_excluded_at, indexable FROM publications WHERE article_id = ?", articleID).Row().Scan(&indexed, &excluded, &visible); err != nil {
		t.Fatal(err)
	}
	if indexed.Valid || !excluded.Valid || visible {
		t.Fatalf("exclude decision indexed=%v excluded=%v indexable=%t", indexed, excluded, visible)
	}
}

func TestSetSEORollsBackDecisionWhenAuditInsertFails(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_seo_rollback_%d", time.Now().UnixNano())
	sourceID, articleID := "source_"+fixture, "article_"+fixture
	articleURL := "https://example.invalid/" + fixture
	actor := "test-refuse-admincontent-seo"
	t.Cleanup(func() {
		_ = orm.Exec("DROP TRIGGER IF EXISTS refuse_admincontent_seo_audit ON audit_log").Error
		_ = orm.Exec("DROP FUNCTION IF EXISTS refuse_admincontent_seo_audit()").Error
		_ = orm.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources (id,name,kind) VALUES (?, 'SEO rollback fixture', 'external')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, ?, 'SEO rollback fixture', now(), now())`, articleID, sourceID, "url:"+articleURL, articleURL).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications (article_id,title,summary,source_id,channel,url,discovered_at,timeline_at,sort_at)
		VALUES (?, 'SEO rollback fixture', 'readable summary', ?, 'news', ?, now(), now(), now())`, articleID, sourceID, articleURL).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`CREATE FUNCTION refuse_admincontent_seo_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.actor = 'test-refuse-admincontent-seo' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("CREATE TRIGGER refuse_admincontent_seo_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION refuse_admincontent_seo_audit()").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetSEO(ctx, articleID, SEOInput{Indexed: true, Reason: "reviewed"}, actor); err == nil {
		t.Fatal("audit failure did not abort the SEO transaction")
	}
	var indexedAt, excludedAt sql.NullTime
	var indexable bool
	if err := orm.Raw("SELECT seo_indexed_at, seo_excluded_at, indexable FROM publications WHERE article_id = ?", articleID).Row().Scan(&indexedAt, &excludedAt, &indexable); err != nil {
		t.Fatal(err)
	}
	if indexedAt.Valid || excludedAt.Valid || indexable {
		t.Fatalf("SEO write escaped failed transaction: indexed=%v excluded=%v indexable=%t", indexedAt, excludedAt, indexable)
	}
}
