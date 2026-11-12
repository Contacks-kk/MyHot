package translations

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

func openTranslationIntegrationRepository(t *testing.T) (*gorm.DB, *GormRepository) {
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

func TestRepositoryTranslationEligibilityAttemptsAndRevisionSafeUpsert(t *testing.T) {
	orm, repository := openTranslationIntegrationRepository(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("translation-source-%d", time.Now().UnixNano())
	articleID := fmt.Sprintf("translation-item-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM translation_attempts WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM translations WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources (id,name,kind,config) VALUES (?, 'Translation fixture', 'rss', '{}')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,language,revision,body_text,body_html,body_status,discovered_at,timeline_at)
		VALUES (?, ?, ?, 'https://example.invalid/translation', 'Original title', 'en', 3, 'Original body', '<p>Original body</p>', 'ok', now(), now())`, articleID, sourceID, articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications (article_id,title,selected,visibility,body_mode,source_id,channel,url,discovered_at,timeline_at)
		VALUES (?, 'Chinese title', true, 'public', 'full', ?, 'news', 'https://example.invalid/translation', now(), now())`, articleID, sourceID).Error; err != nil {
		t.Fatal(err)
	}

	ids, err := repository.DueArticles(ctx, 30)
	if err != nil || len(ids) != 1 || ids[0] != articleID {
		t.Fatalf("due IDs = %v, err=%v", ids, err)
	}
	article, err := repository.Article(ctx, articleID)
	if err != nil || article == nil || article.Revision != 3 || article.Channel != "news" || !article.Selected || *article.BodyText != "Original body" {
		t.Fatalf("article = %#v, err=%v", article, err)
	}
	if err := repository.RecordAttempt(ctx, articleID, 3, "failed", stringPointer("provider error")); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordAttempt(ctx, articleID, 3, "skipped", stringPointer("translation is terminal for this revision")); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := orm.Table("translation_attempts").Select("attempts").Where("article_id = ?", articleID).Scan(&attempts).Error; err != nil || attempts != 2 {
		t.Fatalf("recorded attempts = %d, err=%v", attempts, err)
	}

	changed, err := repository.SaveTranslation(ctx, Translation{ArticleID: articleID, Revision: 3, Title: "标题", BodyHTML: "<p>正文</p>", BodyText: "正文", Complete: true})
	if err != nil || !changed {
		t.Fatalf("current translation save changed=%t err=%v", changed, err)
	}
	changed, err = repository.SaveTranslation(ctx, Translation{ArticleID: articleID, Revision: 2, Title: "旧标题", BodyHTML: "<p>旧正文</p>", BodyText: "旧正文", Complete: true})
	if err != nil || changed {
		t.Fatalf("stale translation save changed=%t err=%v", changed, err)
	}
	var stored struct {
		Revision int
		Title    string
	}
	if err := orm.Table("translations").Select("revision,title").Where("article_id = ? AND lang = 'zh'", articleID).Scan(&stored).Error; err != nil || stored.Revision != 3 || stored.Title != "标题" {
		t.Fatalf("stored translation = %#v, err=%v", stored, err)
	}
	if err := orm.Exec("UPDATE translations SET origin = 'source' WHERE article_id = ? AND lang = 'zh'", articleID).Error; err != nil {
		t.Fatal(err)
	}
	changed, err = repository.SaveTranslation(ctx, Translation{ArticleID: articleID, Revision: 3, Title: "替换", BodyHTML: "<p>替换</p>", BodyText: "替换", Complete: true})
	if err != nil || changed {
		t.Fatalf("source translation was overwritten: changed=%t err=%v", changed, err)
	}
	ids, err = repository.DueArticles(ctx, 30)
	if err != nil || len(ids) != 0 {
		t.Fatalf("article with terminal current-revision attempt remained due: %v err=%v", ids, err)
	}
}

func TestRepositoryQuoteCandidatesAndConflictUpsert(t *testing.T) {
	orm, repository := openTranslationIntegrationRepository(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("translation-quote-source-%d", time.Now().UnixNano())
	articleID := fmt.Sprintf("translation-quote-item-%d", time.Now().UnixNano())
	tweetID := fmt.Sprintf("%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM quote_translations WHERE tweet_id = ?", tweetID).Error
		_ = orm.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = orm.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := orm.Exec("INSERT INTO sources (id,name,kind,config) VALUES (?, 'Translation X fixture', 'x_search', '{}')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	xPost, err := json.Marshal(map[string]any{"text": "Original post", "quoted": map[string]any{
		"text": "A quoted announcement with enough English words to translate.",
		"url":  "https://x.com/example/status/" + tweetID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,language,x_post,discovered_at,timeline_at)
		VALUES (?, ?, ?, 'https://x.com/example/status/1', 'Original post', 'en', ?::jsonb, now(), now())`, articleID, sourceID, articleID, string(xPost)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO publications (article_id,title,selected,visibility,body_mode,source_id,channel,url,discovered_at,timeline_at)
		VALUES (?, 'Original post', true, 'public', 'full', ?, 'x', 'https://x.com/example/status/1', now(), now())`, articleID, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err := repository.QuoteCandidates(ctx, 3)
	if err != nil || len(candidates) != 1 || candidates[0].TweetID != tweetID || !strings.Contains(candidates[0].Text, "quoted announcement") {
		t.Fatalf("quote candidates = %#v, err=%v", candidates, err)
	}
	changed, err := repository.SaveQuoteTranslation(ctx, QuoteTranslation{TweetID: tweetID, TextHash: "first", TextZH: "中文引用", Origin: "model"})
	if err != nil || !changed {
		t.Fatalf("initial quote save changed=%t err=%v", changed, err)
	}
	changed, err = repository.SaveQuoteTranslation(ctx, QuoteTranslation{TweetID: tweetID, TextHash: "first", TextZH: "中文引用", Origin: "model"})
	if err != nil || changed {
		t.Fatalf("identical quote save changed=%t err=%v", changed, err)
	}
	changed, err = repository.SaveQuoteTranslation(ctx, QuoteTranslation{TweetID: tweetID, TextHash: "second", TextZH: "更新译文", Origin: "model"})
	if err != nil || !changed {
		t.Fatalf("updated quote save changed=%t err=%v", changed, err)
	}
}
