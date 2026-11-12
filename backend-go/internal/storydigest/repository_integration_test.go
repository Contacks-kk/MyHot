package storydigest

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

func TestRepositorySavesDigestAtomicallyAndRejectsChangedEvidence(t *testing.T) {
	db := storyDigestTestDB(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("digest_repo_%d", time.Now().UnixNano())
	sourceID, articleID, factPublicID := prefix+"_source", prefix+"_article", prefix+"_fact"
	storyPublicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	var storyID, factID, analysisID int64
	var firstReceipt, staleReceipt int64
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM fact_articles WHERE article_id = ?", articleID).Error
		_ = db.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = db.Exec("DELETE FROM analyses WHERE article_id = ?", articleID).Error
		_ = db.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		if factID != 0 {
			_ = db.Exec("DELETE FROM facts WHERE id = ?", factID).Error
		}
		if storyID != 0 {
			_ = db.Exec("DELETE FROM stories WHERE id = ?", storyID).Error
		}
		_ = db.Exec("DELETE FROM receipts WHERE id IN (?, ?)", firstReceipt, staleReceipt).Error
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := db.Exec("INSERT INTO sources(id,name,kind,participation_mode,tier) VALUES (?, 'Digest fixture', 'external', 'editorial', 'T1')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("INSERT INTO stories(public_id,title,version,origin) VALUES (?::uuid, 'Fixture story', 1, 'model') RETURNING id", storyPublicID).Scan(&storyID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES (?, ?, ?, 'https://fixture.example/report', 'Fixture report', ?, ?)`, articleID, sourceID, articleID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO analyses(article_id,input_revision,origin,relevance,output)
		VALUES (?, 1, 'model', 'pass', '{"scope":"single","fact":{"evidence":"公告原句","conditions":[{"text":"开放范围","quote":"面向用户开放"}]}}'::jsonb) RETURNING id`, articleID).Scan(&analysisID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO publications(article_id,analysis_id,source_id,title,summary,url,channel,published_at,discovered_at,timeline_at,sort_at,visibility,eligible,selected)
		VALUES (?, ?, ?, 'Fixture report', 'Original summary', 'https://fixture.example/report', 'news', ?, ?, ?, ?, 'public', true, false)`, articleID, analysisID, sourceID, now, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("INSERT INTO facts(public_id,story_id,title,subject,action,object,conditions) VALUES (?, ?, 'Fixture fact', 'Company', 'opened', 'product', 'available to users') RETURNING id", factPublicID, storyID).Scan(&factID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO fact_articles(fact_id,article_id,role,evidence) VALUES (?, ?, 'primary', '面向用户开放')", factID, articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,status,request,attempts,origin)
		VALUES (?, 'fixture', 'story_digest', 'received', '{}'::jsonb, 1, 'live') RETURNING id`, prefix+"_first").Scan(&firstReceipt).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(db)
	reports, err := repository.Reports(ctx, storyID, now.Add(time.Hour))
	if err != nil || len(reports) != 1 || reports[0].ID != articleID || len(reports[0].StructuredFact) == 0 {
		t.Fatalf("digest reports = %#v, %v", reports, err)
	}
	hash, err := DigestInputsHash(reports)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := repository.StaleStories(ctx, 100, now.Add(time.Hour))
	if err != nil || !containsInt64(ids, storyID) {
		t.Fatalf("undigested story missing from scanner: %v, %v", ids, err)
	}
	updated, err := repository.Save(ctx, SaveInput{StoryID: storyID, StoryVersion: 1, Version: 2, Title: "Updated story title",
		Digest: "A sufficiently long, grounded story digest.", Latest: reports[0].Title, ArticleIDs: []string{articleID}, InputsHash: hash, ReceiptID: &firstReceipt})
	if err != nil || !updated {
		t.Fatalf("initial digest save = %t, %v", updated, err)
	}
	var persisted struct {
		Title          string `gorm:"column:title"`
		Digest         string `gorm:"column:digest"`
		Version        int    `gorm:"column:version"`
		ReceiptStatus  string `gorm:"column:receipt_status"`
		DigestVersions int    `gorm:"column:digest_versions"`
	}
	if err := db.Raw(`SELECT s.title,s.digest,s.version,r.status AS receipt_status,
		(SELECT count(*)::int FROM story_digests WHERE story_id=s.id) AS digest_versions
		FROM stories s CROSS JOIN receipts r WHERE s.id=? AND r.id=?`, storyID, firstReceipt).Scan(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Title != "Updated story title" || persisted.Digest != "A sufficiently long, grounded story digest." || persisted.Version != 2 || persisted.ReceiptStatus != "completed" || persisted.DigestVersions != 1 {
		t.Fatalf("persisted digest state = %#v", persisted)
	}
	if err := db.Exec("UPDATE publications SET summary = 'Corrected summary', updated_at = now() WHERE article_id = ?", articleID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,status,request,attempts,origin)
		VALUES (?, 'fixture', 'story_digest', 'received', '{}'::jsonb, 1, 'live') RETURNING id`, prefix+"_stale").Scan(&staleReceipt).Error; err != nil {
		t.Fatal(err)
	}
	updated, err = repository.Save(ctx, SaveInput{StoryID: storyID, StoryVersion: 2, Version: 3, Title: "Stale title",
		Digest: "Stale digest should not be stored.", Latest: reports[0].Title, ArticleIDs: []string{articleID}, InputsHash: hash, ReceiptID: &staleReceipt})
	if err != nil || updated {
		t.Fatalf("changed evidence accepted stale model output: updated=%t err=%v", updated, err)
	}
	if err := db.Exec("UPDATE publications SET visibility = 'withdrawn', updated_at = now() WHERE article_id = ?", articleID).Error; err != nil {
		t.Fatal(err)
	}
	cleared, err := repository.Clear(ctx, storyID, 2)
	if err != nil || !cleared {
		t.Fatalf("withdrawn story digest clear = %t, %v", cleared, err)
	}
	var state struct {
		Digest          *string    `gorm:"column:digest"`
		Latest          *string    `gorm:"column:latest"`
		DigestUpdatedAt *time.Time `gorm:"column:digest_updated_at"`
	}
	if err := db.Model(&struct{}{}).Table("stories").Select("digest,latest,digest_updated_at").Where("id = ?", storyID).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Digest != nil || state.Latest != nil || state.DigestUpdatedAt != nil {
		t.Fatalf("cleared digest projection remains: %#v", state)
	}
}

func storyDigestTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
