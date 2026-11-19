package articlebody

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
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
	body := &Extracted{HTML: "<p>Readable article body</p>", Text: "Readable article body", PublishedAt: &published, Images: []Image{}}
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

/** TestStaleExtractionResultsCannotChangeNewerMaterial protects late page and X Article responses. */
func TestStaleExtractionResultsCannotChangeNewerMaterial(t *testing.T) {
	db := articleBodyTestDB(t)
	for _, test := range []struct {
		name  string
		x     bool
		empty bool
	}{
		{name: "page body"},
		{name: "empty page response", empty: true},
		{name: "X Article body", x: true},
		{name: "empty X Article response", x: true, empty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := fmt.Sprintf("article_body_stale_%d", time.Now().UnixNano())
			sourceID, articleID := prefix+"_source", prefix+"_article"
			cleanupArticleBodyFixture(t, db, sourceID, articleID)
			kind := "rss"
			url := "https://example.invalid/stale/" + prefix
			if test.x {
				kind = "x_search"
				url = "https://x.com/author/status/" + prefix
			}
			if err := db.Exec(`INSERT INTO sources(id,name,kind,participation_mode,config) VALUES (?, 'Stale extraction fixture', ?, 'editorial', '{}')`, sourceID, kind).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Microsecond)
			oldHash := materials.ContentHash("Original title", "", "Original excerpt")
			newHash := materials.ContentHash("Corrected report", "Complete corrected body", "Original excerpt")
			if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,excerpt,content_hash,body_text,body_status,revision,processing_state,processing_attempts,processing_error,created_at,discovered_at,timeline_at,media)
				VALUES (?, ?, ?, ?, 'Corrected report', 'Original excerpt', ?, 'Complete corrected body', 'ok', 2, 'new', 0, NULL, ?, ?, ?, '[]'::jsonb)`,
				articleID, sourceID, articleID, url, newHash, now, now, now).Error; err != nil {
				t.Fatal(err)
			}
			for _, revision := range []struct {
				number int
				hash   string
				title  string
			}{
				{number: 1, hash: oldHash, title: "Original title"},
				{number: 2, hash: newHash, title: "Corrected report"},
			} {
				if err := db.Create(&model.ArticleRevision{ArticleID: articleID, Revision: revision.number, ContentHash: revision.hash, Title: revision.title}).Error; err != nil {
					t.Fatal(err)
				}
			}

			var body *Extracted
			if !test.empty {
				body = &Extracted{HTML: "<p>Stale response</p>", Text: "Stale response", Images: []Image{}}
				if test.x {
					body.XArticle = json.RawMessage(`{"title":"Stale X Article"}`)
				}
			}
			changed, err := NewRepository(db).SaveBody(context.Background(), Candidate{ID: articleID, Revision: 1}, body)
			if err != nil || changed {
				t.Fatalf("stale extraction SaveBody() = %t, %v", changed, err)
			}
			attempts, terminal, err := NewRepository(db).RecordFailure(context.Background(), articleID, 1, "late extraction failure")
			if err != nil || attempts != 0 || terminal {
				t.Fatalf("stale extraction RecordFailure() = attempts %d, terminal %t, err %v", attempts, terminal, err)
			}

			var current struct {
				Title           string          `gorm:"column:title"`
				BodyText        string          `gorm:"column:body_text"`
				BodyStatus      string          `gorm:"column:body_status"`
				Revision        int             `gorm:"column:revision"`
				ProcessingState string          `gorm:"column:processing_state"`
				ProcessingTries int             `gorm:"column:processing_attempts"`
				ProcessingError *string         `gorm:"column:processing_error"`
				ProcessingRetry *time.Time      `gorm:"column:processing_retry_at"`
				XArticle        json.RawMessage `gorm:"column:x_article;type:jsonb"`
			}
			if err := db.Table("articles").Where("id = ?", articleID).Take(&current).Error; err != nil {
				t.Fatal(err)
			}
			if current.Title != "Corrected report" || current.BodyText != "Complete corrected body" || current.BodyStatus != "ok" || current.Revision != 2 || current.ProcessingState != "new" || current.ProcessingTries != 0 || current.ProcessingError != nil || current.ProcessingRetry != nil || len(current.XArticle) != 0 {
				t.Fatalf("stale extraction changed newer material: %#v", current)
			}
		})
	}
}

/** TestOverlappingBodyExtractionCreatesOneRevision keeps concurrent workers and manual retries idempotent. */
func TestOverlappingBodyExtractionCreatesOneRevision(t *testing.T) {
	db := articleBodyTestDB(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("article_body_overlap_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	cleanupArticleBodyFixture(t, db, sourceID, articleID)
	if err := db.Exec(`INSERT INTO sources(id,name,kind,participation_mode,config) VALUES (?, 'Overlapping extraction fixture', 'rss', 'editorial', '{}')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldHash := materials.ContentHash("Overlap title", "", "")
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,content_hash,body_status,revision,created_at,discovered_at,timeline_at,media)
		VALUES (?, ?, ?, 'https://example.invalid/overlap', 'Overlap title', ?, 'pending', 1, ?, ?, ?, '[]'::jsonb)`,
		articleID, sourceID, articleID, oldHash, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ArticleRevision{ArticleID: articleID, Revision: 1, ContentHash: oldHash, Title: "Overlap title"}).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(db)
	candidate := Candidate{ID: articleID, Revision: 1, BodyStatus: "pending"}
	body := &Extracted{HTML: "<p>Publisher body</p>", Text: "Publisher body", Images: []Image{}}
	results := make(chan struct {
		changed bool
		err     error
	}, 2)
	for range 2 {
		go func() {
			changed, err := repository.SaveBody(ctx, candidate, body)
			results <- struct {
				changed bool
				err     error
			}{changed: changed, err: err}
		}()
	}
	changes := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.changed {
			changes++
		}
	}
	if changes != 1 {
		t.Fatalf("overlapping extraction revisions committed=%d, want 1", changes)
	}
	if err := db.Exec(`UPDATE articles SET body_status='pending' WHERE id=?`, articleID).Error; err != nil {
		t.Fatal(err)
	}
	changed, err := repository.SaveBody(ctx, Candidate{ID: articleID, Revision: 2, BodyStatus: "pending"}, body)
	if err != nil || changed {
		t.Fatalf("unchanged retry SaveBody() = %t, %v", changed, err)
	}
	var article struct {
		Revision   int     `gorm:"column:revision"`
		BodyStatus string  `gorm:"column:body_status"`
		BodyText   *string `gorm:"column:body_text"`
	}
	if err := db.Table("articles").Where("id = ?", articleID).Take(&article).Error; err != nil {
		t.Fatal(err)
	}
	var revisions int64
	if err := db.Model(&model.ArticleRevision{}).Where("article_id = ?", articleID).Count(&revisions).Error; err != nil {
		t.Fatal(err)
	}
	if article.Revision != 2 || article.BodyStatus != "ok" || article.BodyText == nil || *article.BodyText != body.Text || revisions != 2 {
		t.Fatalf("overlapping extraction or unchanged retry changed material: article=%#v revisions=%d", article, revisions)
	}
}

/** TestSaveBodyLeavesConfirmedMaterialUntouched preserves publisher-confirmed body content. */
func TestSaveBodyLeavesConfirmedMaterialUntouched(t *testing.T) {
	db := articleBodyTestDB(t)
	prefix := fmt.Sprintf("article_body_confirmed_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	cleanupArticleBodyFixture(t, db, sourceID, articleID)
	if err := db.Exec(`INSERT INTO sources(id,name,kind,participation_mode,config) VALUES (?, 'Confirmed body fixture', 'rss', 'editorial', '{}')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	original := "Publisher-confirmed article body"
	hash := materials.ContentHash("Confirmed title", original, "")
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,body_text,content_hash,body_status,revision,discovered_at,timeline_at,media)
		VALUES (?, ?, ?, 'https://example.invalid/confirmed', 'Confirmed title', ?, ?, 'ok', 1, ?, ?, '[]'::jsonb)`,
		articleID, sourceID, articleID, original, hash, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ArticleRevision{ArticleID: articleID, Revision: 1, ContentHash: hash, Title: "Confirmed title", BodyText: &original}).Error; err != nil {
		t.Fatal(err)
	}

	// A later page rendering may enrich metadata, but cannot replace confirmed content.
	changed, err := NewRepository(db).SaveBody(context.Background(), Candidate{ID: articleID, Revision: 1, BodyStatus: "ok"}, &Extracted{
		HTML:   "<p>Different page rendering</p>",
		Text:   "Different page rendering",
		Images: []Image{},
	})
	if err != nil || changed {
		t.Fatalf("SaveBody() = %t, %v; confirmed content must be skipped", changed, err)
	}
	var article struct {
		BodyText   string `gorm:"column:body_text"`
		BodyStatus string `gorm:"column:body_status"`
		Revision   int    `gorm:"column:revision"`
	}
	if err := db.Table("articles").Where("id = ?", articleID).Take(&article).Error; err != nil {
		t.Fatal(err)
	}
	if article.BodyText != original || article.BodyStatus != "ok" || article.Revision != 1 {
		t.Fatalf("confirmed article changed: %#v", article)
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
	var savedArticle, expectedArticle any
	if err := json.Unmarshal(saved.XArticle, &savedArticle); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(articleJSON, &expectedArticle); err != nil {
		t.Fatal(err)
	}
	if saved.Title != "Long form" || saved.BodyText != bodyText || !reflect.DeepEqual(savedArticle, expectedArticle) || saved.Revision != 2 || saved.BodyStatus != "ok" {
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
