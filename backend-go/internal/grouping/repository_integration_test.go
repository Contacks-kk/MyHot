package grouping

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

func openGroupingTestDB(t *testing.T) (*gorm.DB, *GormRepository) {
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
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, NewRepository(db)
}

func TestRecallRepositoryFiltersLiveRecentNonCompositeEvidence(t *testing.T) {
	db, repository := openGroupingTestDB(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("grouping_recall_%d", time.Now().UnixNano())
	sourceID := "source_" + fixture
	liveStory, compositeStory, oldStory, mergedStory, mergeTarget := int64(0), int64(0), int64(0), int64(0), int64(0)
	ids := map[string]string{}
	cleanup := func() {
		for _, id := range ids {
			_ = db.Exec("DELETE FROM publications WHERE article_id = ?", id).Error
			_ = db.Exec("DELETE FROM fact_articles WHERE article_id = ?", id).Error
			_ = db.Exec("DELETE FROM analyses WHERE article_id = ?", id).Error
			_ = db.Exec("DELETE FROM articles WHERE id = ?", id).Error
		}
		for _, id := range []int64{liveStory, compositeStory, oldStory, mergedStory, mergeTarget} {
			if id != 0 {
				_ = db.Exec("DELETE FROM facts WHERE story_id = ?", id).Error
				_ = db.Exec("DELETE FROM story_links WHERE story_id = ? OR other_id = ?", id, id).Error
				_ = db.Exec("DELETE FROM stories WHERE id = ?", id).Error
			}
		}
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	}
	t.Cleanup(cleanup)
	if err := db.Exec("INSERT INTO sources (id,name,kind) VALUES (?, 'Grouping recall fixture', 'rss')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	newStory := func(label string) int64 {
		t.Helper()
		publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
		var id int64
		if err := db.Raw("INSERT INTO stories (public_id,title) VALUES (?,?) RETURNING id", publicID, label).Scan(&id).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	liveStory, compositeStory, oldStory, mergedStory, mergeTarget = newStory("live"), newStory("composite"), newStory("old"), newStory("merged"), newStory("target")
	if err := db.Exec("UPDATE stories SET merged_into = ? WHERE id = ?", mergeTarget, mergedStory).Error; err != nil {
		t.Fatal(err)
	}
	insertEvidence := func(name string, storyID int64, discovered time.Time, composite bool) string {
		t.Helper()
		articleID := "article_" + fixture + "_" + name
		articleURL := "https://example.invalid/" + articleID
		ids[name] = articleID
		if err := db.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES (?,?,?,?,?,?,?)`, articleID, sourceID, "url:"+articleURL, articleURL, name, discovered, discovered).Error; err != nil {
			t.Fatal(err)
		}
		var factID int64
		if err := db.Raw("INSERT INTO facts (public_id,story_id,title) VALUES (?,?,?) RETURNING id", fixture+"_"+name, storyID, name).Scan(&factID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?,?,'report')", factID, articleID).Error; err != nil {
			t.Fatal(err)
		}
		if composite {
			if err := db.Exec(`INSERT INTO analyses (article_id,input_revision,origin,output) VALUES (?,1,'model','{"scope":"composite"}')`, articleID).Error; err != nil {
				t.Fatal(err)
			}
		}
		return articleID
	}
	now := time.Now().UTC()
	goodID := insertEvidence("good", liveStory, now.Add(-time.Hour), false)
	insertEvidence("composite", compositeStory, now.Add(-time.Hour), true)
	insertEvidence("old", oldStory, now.Add(-15*24*time.Hour), false)
	insertEvidence("merged", mergedStory, now.Add(-time.Hour), false)

	rows, err := repository.RecallPool(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ArticleID != goodID || rows[0].Revision != 0 {
		t.Fatalf("recall eligibility drifted from TypeScript: %#v", rows)
	}
}

func TestRecallRepositorySelectedContextAndRelatedXIdentity(t *testing.T) {
	db, repository := openGroupingTestDB(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("grouping_context_%d", time.Now().UnixNano())
	sourceID := "source_" + fixture
	selectedID, futureID, factIDArticle, referencedID, candidateID := "article_"+fixture+"_selected", "article_"+fixture+"_future", "article_"+fixture+"_fact", "article_"+fixture+"_ref", "article_"+fixture+"_candidate"
	var storyID, factID int64
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM publications WHERE article_id IN ?", []string{selectedID, futureID, factIDArticle, referencedID, candidateID}).Error
		_ = db.Exec("DELETE FROM fact_articles WHERE article_id IN ?", []string{selectedID, futureID, factIDArticle, referencedID, candidateID}).Error
		_ = db.Exec("DELETE FROM analyses WHERE article_id IN ?", []string{selectedID, futureID, factIDArticle, referencedID, candidateID}).Error
		_ = db.Exec("DELETE FROM articles WHERE id IN ?", []string{selectedID, futureID, factIDArticle, referencedID, candidateID}).Error
		if storyID != 0 {
			_ = db.Exec("DELETE FROM facts WHERE story_id = ?", storyID).Error
			_ = db.Exec("DELETE FROM stories WHERE id = ?", storyID).Error
		}
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := db.Exec("INSERT INTO sources (id,name,kind,tier) VALUES (?, 'Grouping context fixture', 'rss', 'T1')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	if err := db.Raw("INSERT INTO stories (public_id,title) VALUES (?,'context') RETURNING id", publicID).Scan(&storyID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("INSERT INTO facts (public_id,story_id,title) VALUES (?,?,'context fact') RETURNING id", fixture+"_fact", storyID).Scan(&factID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insertArticle := func(id, identity, label string, selected bool, visibleAfter time.Time, publicationFact *int64) {
		t.Helper()
		articleURL := "https://example.invalid/" + id
		if err := db.Exec(`INSERT INTO articles (id,source_id,identity_key,url,title,discovered_at,timeline_at,body_text)
			VALUES (?,?,?,?,?,?,?,?)`, id, sourceID, identity, articleURL, label, now, now, strings.Repeat("body", 2000)).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO publications (article_id,title,summary,source_id,channel,url,discovered_at,timeline_at,selected,visible_after,fact_id)
			VALUES (?,?,?,?,'news',?,?,?,?,?,?)`, id, label+" public", "summary", sourceID, articleURL, now, now, selected, visibleAfter, publicationFact).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertArticle(selectedID, "url:"+selectedID, "selected", true, now.Add(-time.Minute), nil)
	insertArticle(futureID, "url:"+futureID, "future", true, now.Add(time.Hour), nil)
	insertArticle(factIDArticle, "url:"+factIDArticle, "has fact", true, now.Add(-time.Minute), &factID)
	insertArticle(referencedID, "x:tweet-123", "referenced", false, now, nil)
	insertArticle(candidateID, "url:"+candidateID, "candidate", true, now.Add(-time.Minute), &factID)
	if err := db.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?,?,'report')", factID, referencedID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO fact_articles (fact_id,article_id,role) VALUES (?,?,'primary')", factID, candidateID).Error; err != nil {
		t.Fatal(err)
	}

	selected, err := repository.SelectedReadingPool(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].ArticleID != selectedID || selected[0].Title != "selected public" || !selected[0].FirstParty {
		t.Fatalf("selected reading context gate changed: %#v", selected)
	}
	texts, err := repository.ReportTexts(ctx, []string{selectedID, referencedID})
	if err != nil || texts[selectedID] != "selected public。summary" || texts[referencedID] != "referenced public。summary" {
		t.Fatalf("report text projection changed: %#v err=%v", texts, err)
	}
	bodies, err := repository.ArticleBodies(ctx, []string{selectedID})
	if err != nil || bodies[selectedID] == nil || len([]rune(*bodies[selectedID])) != 8000 {
		t.Fatalf("article body bounded read changed: %#v err=%v", bodies, err)
	}
	sameURL, referenced, err := repository.RelatedPosts(ctx, "article_query_"+fixture, "https://example.invalid/no-match", []string{"tweet-123"})
	if err != nil || sameURL != nil || len(referenced) != 1 || referenced[0].ArticleID != referencedID {
		t.Fatalf("X reply/quote identity changed: same=%#v referenced=%#v err=%v", sameURL, referenced, err)
	}
	candidates, err := repository.CandidateViews(ctx, []Recalled{{FactID: factID, Score: .9}}, nil, now)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidate representative assembly failed: candidates=%#v err=%v", candidates, err)
	}
	candidate := candidates[0]
	if candidate.FactID != factID || candidate.StoryID != storyID || candidate.FactTitle != "context fact" || candidate.Members != 2 || !candidate.StoryRoot || candidate.Score != .9 || !candidate.Selected || candidate.Report.Title != "candidate public" || candidate.SelectedReport == nil || candidate.SelectedReport.Title != "candidate public" {
		t.Fatalf("candidate identity/selected projection changed: %#v", candidate)
	}
}

func TestEmbeddingRepositoryRoundTripsRealArrayAndCompletesReceiptAtomically(t *testing.T) {
	db, repository := openGroupingTestDB(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("grouping_embedding_%d", time.Now().UnixNano())
	modelName, articleID := "embedding-fixture-model", "article_"+fixture
	var receiptID int64
	if err := db.Raw(`INSERT INTO receipts (logical_key,service,model,purpose,status,attempts)
		VALUES (?, 'embedding', ?, 'embedding', 'received', 1) RETURNING id`, fixture, modelName).Scan(&receiptID).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM embeddings WHERE kind = 'article' AND model = ? AND ref_id = ?", modelName, articleID).Error
		_ = db.Exec("DELETE FROM receipts WHERE id = ?", receiptID).Error
	})
	want := []float32{1.25, -2.5, 3}
	if err := repository.SaveEmbeddingsAndComplete(ctx, modelName, []StoredEmbedding{{ID: articleID, Hash: "fixture-hash", Vector: want}}, receiptID); err != nil {
		t.Fatal(err)
	}
	rows, err := repository.LoadEmbeddings(ctx, modelName, []string{articleID})
	if err != nil || len(rows) != 1 || rows[articleID].Hash != "fixture-hash" || len(rows[articleID].Vector) != len(want) {
		t.Fatalf("embedding array round trip failed: rows=%#v err=%v", rows, err)
	}
	for index := range want {
		if rows[articleID].Vector[index] != want[index] {
			t.Fatalf("vector[%d]=%v want %v", index, rows[articleID].Vector[index], want[index])
		}
	}
	var status string
	if err := db.Raw("SELECT status FROM receipts WHERE id = ?", receiptID).Scan(&status).Error; err != nil || status != "completed" {
		t.Fatalf("embedding receipt was not completed with its vectors: status=%q err=%v", status, err)
	}
}
