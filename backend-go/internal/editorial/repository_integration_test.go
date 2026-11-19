package editorial

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func editorialTestDB(t *testing.T) *gorm.DB {
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

func TestRepositorySaveAnalysisCommitsReceiptsAndStaleRevisionAtomically(t *testing.T) {
	db := editorialTestDB(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("editorial_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	firstKey, staleKey := prefix+"_first", prefix+"_stale"
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
		_ = db.Exec("DELETE FROM receipts WHERE logical_key IN (?,?)", firstKey, staleKey).Error
	})
	if err := db.Exec("INSERT INTO sources(id,name,kind,participation_mode) VALUES(?, 'Editorial fixture', 'rss', 'editorial')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,body_status,revision,processing_state)
		VALUES(?,?,?,'https://publisher.example/story','Editorial fixture',?,?,'ok',1,'new')`, articleID, sourceID, articleID, at, at).Error; err != nil {
		t.Fatal(err)
	}
	insertReceipt := func(key string) int64 {
		t.Helper()
		var id int64
		if err := db.Raw(`INSERT INTO receipts(logical_key,service,model,purpose,subject,status) VALUES(?, 'editorial-test', 'fixture', 'prefilter_article', ?, 'received') RETURNING id`, key, "article:"+articleID+"@1").Row().Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	firstReceipt := insertReceipt(firstKey)
	input := Input{ID: articleID, Revision: 1}
	output := AnalysisOutput{Relevance: "pass", Selected: true, Tags: []string{"模型发布"}, Subjects: []string{"openai"}, TitleZh: "标题", SummaryZh: "摘要", Scope: "single"}
	run := AnalysisRun{Prefilter: PrefilterResult{Label: "PASS", Reason: "有证据", Model: "fixture", ReceiptID: firstReceipt}}
	result, err := NewRepository(db).SaveAnalysis(ctx, input, run, output, "fixture-prompt")
	if err != nil {
		t.Fatal(err)
	}
	if result.AnalysisID == nil || result.Stale || result.Output == nil || result.Output.Relevance != "pass" || len(result.ReceiptIDs) != 1 || result.ReceiptIDs[0] != firstReceipt {
		t.Fatalf("saved analysis result = %#v", result)
	}
	var status string
	if err := db.Raw("SELECT status FROM receipts WHERE id = ?", firstReceipt).Scan(&status).Error; err != nil || status != "completed" {
		t.Fatalf("receipt status = %q: %v", status, err)
	}
	var state string
	if err := db.Raw("SELECT processing_state FROM articles WHERE id = ?", articleID).Scan(&state).Error; err != nil || state != "analyzed" {
		t.Fatalf("article processing state = %q: %v", state, err)
	}

	staleReceipt := insertReceipt(staleKey)
	if err := db.Model(&model.ArticleProcessing{}).Where("id = ?", articleID).Updates(map[string]any{
		"revision": 2, "processing_state": "new", "processing_error": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	run.Prefilter.ReceiptID = staleReceipt
	stale, err := NewRepository(db).SaveAnalysis(ctx, input, run, output, "fixture-prompt")
	if err != nil {
		t.Fatal(err)
	}
	if stale.AnalysisID == nil || !stale.Stale {
		t.Fatalf("stale revision result = %#v", stale)
	}
	if err := db.Raw("SELECT processing_state FROM articles WHERE id = ?", articleID).Scan(&state).Error; err != nil || state != "new" {
		t.Fatalf("stale analysis changed the pending revision state to %q: %v", state, err)
	}
}

func TestRepositorySaveAnalysisRollsBackReceiptOnInvalidAnalysis(t *testing.T) {
	db := editorialTestDB(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("editorial_rollback_%d", time.Now().UnixNano())
	sourceID, articleID, key := prefix+"_source", prefix+"_article", prefix+"_receipt"
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
		_ = db.Exec("DELETE FROM receipts WHERE logical_key = ?", key).Error
	})
	if err := db.Exec("INSERT INTO sources(id,name,kind) VALUES(?, 'Editorial rollback fixture', 'rss')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,body_status,revision)
		VALUES(?,?,?,'https://publisher.example/rollback','Editorial fixture',?,?,'ok',1)`, articleID, sourceID, articleID, at, at).Error; err != nil {
		t.Fatal(err)
	}
	var receiptID int64
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,subject,status) VALUES(?, 'editorial-test', 'prefilter_article', ?, 'received') RETURNING id`, key, "article:"+articleID+"@1").Row().Scan(&receiptID); err != nil {
		t.Fatal(err)
	}
	_, err := NewRepository(db).SaveAnalysis(ctx, Input{ID: articleID, Revision: 1}, AnalysisRun{
		Prefilter: PrefilterResult{Label: "INVALID", Model: "fixture", ReceiptID: receiptID},
	}, AnalysisOutput{Relevance: "invalid", Tags: []string{}, Subjects: []string{}}, "fixture-prompt")
	if err == nil {
		t.Fatal("invalid analysis unexpectedly committed")
	}
	var status string
	if err := db.Raw("SELECT status FROM receipts WHERE id = ?", receiptID).Scan(&status).Error; err != nil || status != "received" {
		t.Fatalf("receipt was not rolled back with analysis: status=%q err=%v", status, err)
	}
	var count int64
	if err := db.Model(&model.ArticleAnalysis{}).Where("article_id = ?", articleID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("analysis rollback count=%d err=%v", count, err)
	}
}
