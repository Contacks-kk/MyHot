package articlebody

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
	"aihot/backend-go/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func articleBodyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
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

func TestSaveBodyCommitsRevisionAndTimelineAtomically(t *testing.T) {
	db := articleBodyTestDB(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("article_body_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	cleanupArticleBodyFixture(t, db, sourceID, articleID)
	if err := db.Exec(`INSERT INTO sources(id,name,kind,participation_mode,config) VALUES (?, 'Article body fixture', 'rss', 'editorial', '{"fetchPublicContent":true}')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	discovered := time.Now().UTC().Truncate(time.Microsecond)
	oldHash := materials.ContentHash("Article title", "", "feed excerpt")
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,excerpt,content_hash,body_status,revision,created_at,discovered_at,timeline_at,media)
		VALUES (?, ?, ?, 'https://example.invalid/article', 'Article title', 'feed excerpt', ?, 'pending', 1, ?, ?, ?, '[]'::jsonb)`,
		articleID, sourceID, articleID, oldHash, discovered.Add(-4*time.Minute), discovered, discovered).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ArticleRevision{ArticleID: articleID, Revision: 1, ContentHash: oldHash, Title: "Article title"}).Error; err != nil {
		t.Fatal(err)
	}
	published := discovered.Add(-time.Hour)
	body := &Extracted{HTML: "<p>Readable article body</p>", Text: "Readable article body", Images: []Image{}}
	candidate := Candidate{ID: articleID, Revision: 1, BodyStatus: "pending", SourceConfig: []byte(`{"fetchPublicContent":true}`)}
	repository := NewRepository(db)
	pending, err := repository.Pending(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range pending {
		if item.ID == articleID {
			found = !item.HasPublication && item.Bare == true && item.SourceKind == "rss"
		}
	}
	if !found {
		t.Fatal("River candidate scan omitted the unpublished pending page")
	}
	changed, err := repository.SaveBody(ctx, candidate, body)
	if err != nil || !changed {
		t.Fatalf("SaveBody() = %t, %v", changed, err)
	}
	var article struct {
		Revision       int        `gorm:"column:revision"`
		BodyStatus     string     `gorm:"column:body_status"`
		BodyText       *string    `gorm:"column:body_text"`
		PublishedAt    *time.Time `gorm:"column:published_at"`
		PublishedClaim *time.Time `gorm:"column:published_at_claim"`
		Backfill       bool       `gorm:"column:backfill"`
	}
	if err := db.Table("articles").Where("id = ?", articleID).Take(&article).Error; err != nil {
		t.Fatal(err)
	}
	if article.Revision != 2 || article.BodyStatus != "ok" || article.BodyText == nil || *article.BodyText != body.Text || article.PublishedAt == nil || !article.PublishedAt.Equal(published) || article.PublishedClaim == nil || !article.PublishedClaim.Equal(published) || article.Backfill {
		t.Fatalf("unexpected saved article: %#v", article)
	}
	var revisions int64
	if err := db.Model(&model.ArticleRevision{}).Where("article_id = ?", articleID).Count(&revisions).Error; err != nil || revisions != 2 {
		t.Fatalf("revisions = %d, %v", revisions, err)
	}
}

func TestSaveBodyRevisionConflictRollsBackArticleUpdate(t *testing.T) {
	db := articleBodyTestDB(t)
	prefix := fmt.Sprintf("article_body_rollback_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	cleanupArticleBodyFixture(t, db, sourceID, articleID)
	if err := db.Exec(`INSERT INTO sources(id,name,kind,participation_mode,config) VALUES (?, 'Article body rollback fixture', 'rss', 'editorial', '{}')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldHash := materials.ContentHash("Rollback title", "", "")
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,content_hash,body_status,revision,discovered_at,timeline_at,media)
		VALUES (?, ?, ?, 'https://example.invalid/rollback', 'Rollback title', ?, 'pending', 1, ?, ?, '[]'::jsonb)`,
		articleID, sourceID, articleID, oldHash, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ArticleRevision{ArticleID: articleID, Revision: 1, ContentHash: oldHash, Title: "Rollback title"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ArticleRevision{ArticleID: articleID, Revision: 2, ContentHash: "reserved", Title: "Reserved revision"}).Error; err != nil {
		t.Fatal(err)
	}
	body := &Extracted{HTML: "<p>New body that must roll back</p>", Text: "New body that must roll back", Images: []Image{}}
	_, err := NewRepository(db).SaveBody(context.Background(), Candidate{ID: articleID, Revision: 1}, body)
	if err == nil {
		t.Fatal("expected duplicate revision failure")
	}
	var article struct {
		Revision   int     `gorm:"column:revision"`
		BodyStatus string  `gorm:"column:body_status"`
		BodyText   *string `gorm:"column:body_text"`
	}
	if err := db.Table("articles").Where("id = ?", articleID).Take(&article).Error; err != nil {
		t.Fatal(err)
	}
	if article.Revision != 1 || article.BodyStatus != "pending" || article.BodyText != nil {
		t.Fatalf("article update was not rolled back: %#v", article)
	}
}

func TestPendingAndSaveBodyIncludeConfiguredXArticles(t *testing.T) {
	db := articleBodyTestDB(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("article_body_x_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	cleanupArticleBodyFixture(t, db, sourceID, articleID)
	if err := db.Exec(`INSERT INTO sources(id,name,kind,participation_mode,config) VALUES (?, 'X Article fixture', 'x_search', 'editorial', '{}')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldBody := "https://x.com/i/article/123"
	oldHash := materials.ContentHash("X post", oldBody, "")
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,body_text,content_hash,x_post,body_status,revision,created_at,discovered_at,timeline_at,media)
		VALUES (?, ?, ?, 'https://x.com/author/status/456', 'X post', ?, ?, '{"tweetId":"456","text":"https://x.com/i/article/123"}'::jsonb, 'pending', 1, ?, ?, ?, '[]'::jsonb)`,
		articleID, sourceID, articleID, oldBody, oldHash, now.Add(-4*time.Minute), now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ArticleRevision{ArticleID: articleID, Revision: 1, ContentHash: oldHash, Title: "X post", BodyText: &oldBody}).Error; err != nil {
		t.Fatal(err)
	}
	withoutSocialData, err := NewRepository(db).Pending(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range withoutSocialData {
		if candidate.ID == articleID {
			t.Fatal("X Article was scanned without a configured provider")
		}
	}
	repository := NewRepository(db, true)
	pending, err := repository.Pending(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range pending {
		if candidate.ID == articleID {
			found = candidate.XPostPresent && string(candidate.XPost) != "" && candidate.XArticle == nil
		}
	}
	if !found {
		t.Fatal("River scan omitted the unpublished X Article")
	}
	articleJSON := json.RawMessage(`{"title":"Long form","content_state":{"blocks":[{"type":"unstyled","text":"Body"}]}}`)
	bodyText := oldBody + "\n\n# Long form\n\nBody"
	changed, err := repository.SaveBody(ctx, Candidate{ID: articleID, Revision: 1}, &Extracted{Text: bodyText, Title: "Long form", XArticle: articleJSON})
	if err != nil || !changed {
		t.Fatalf("SaveBody() = %t, %v", changed, err)
	}
	var saved struct {
		Title      string          `gorm:"column:title"`
		BodyText   string          `gorm:"column:body_text"`
		XArticle   json.RawMessage `gorm:"column:x_article;type:jsonb"`
		Revision   int             `gorm:"column:revision"`
		BodyStatus string          `gorm:"column:body_status"`
	}
	if err := db.Table("articles").Where("id = ?", articleID).Take(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Title != "Long form" || saved.BodyText != bodyText || string(saved.XArticle) != string(articleJSON) || saved.Revision != 2 || saved.BodyStatus != "ok" {
		t.Fatalf("X Article body was not committed atomically: %#v", saved)
	}
}

func cleanupArticleBodyFixture(t *testing.T, db *gorm.DB, sourceID, articleID string) {
	t.Helper()
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM article_revisions WHERE article_id = ?", articleID).Error
		_ = db.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
}
