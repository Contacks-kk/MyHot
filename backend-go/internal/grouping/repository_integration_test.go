package grouping

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/receipts"
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

/** TestSignalRevisitQueriesKeepOnlyUnattachedRecentDiscussionPosts verifies the legacy eligibility gates. */
func TestSignalRevisitQueriesKeepOnlyUnattachedRecentDiscussionPosts(t *testing.T) {
	db, repository := openGroupingTestDB(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("grouping_signal_revisit_%d", time.Now().UnixNano())
	sourceID := "source_" + fixture
	now := time.Now().UTC().Truncate(time.Second)
	ids := map[string]string{}
	var storyID int64
	t.Cleanup(func() {
		for _, id := range ids {
			_ = db.Exec("DELETE FROM articles WHERE id=?", id).Error
		}
		if storyID != 0 {
			_ = db.Exec("DELETE FROM stories WHERE id=?", storyID).Error
		}
		_ = db.Exec("DELETE FROM sources WHERE id=?", sourceID).Error
	})
	if err := db.Exec(`INSERT INTO sources(id,name,kind,participation_mode)
		VALUES (?, 'Signal revisit fixture', 'x_search', 'hot_signal')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	if err := db.Raw("INSERT INTO stories(public_id,title) VALUES (?,'attached fixture') RETURNING id", publicID).Scan(&storyID).Error; err != nil {
		t.Fatal(err)
	}
	insert := func(name string, discovered time.Time, backfill bool, published *time.Time, xPost string) string {
		t.Helper()
		id := "article_" + fixture + "_" + name
		ids[name] = id
		articleURL := "https://example.invalid/" + id
		if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,
			backfill,published_at,processing_state,x_post,body_text)
			VALUES (?,?,?,?,?,?,?,?,?,'skipped',?::jsonb,'reaction body')`,
			id, sourceID, "url:"+articleURL, articleURL, name, discovered, discovered, backfill, published, xPost).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	replyID := insert("reply", now.Add(-time.Hour), false, nil, `{"replyTo":"tweet-123"}`)
	quoteID := insert("quote", now.Add(-2*time.Hour), false, nil, `{"quoted":{"url":"https://x.com/acme/status/tweet-123"}}`)
	insert("unmatched", now.Add(-3*time.Hour), false, nil, `{"replyTo":"tweet-123"}`)
	insert("other-recent", now.Add(-3*time.Hour), false, nil, `{}`)
	insert("too-old", now.Add(-7*time.Hour), false, nil, `{"replyTo":"tweet-123"}`)
	stalePublished := now.Add(-50 * time.Hour)
	insert("stale-backfill", now.Add(-time.Hour), true, &stalePublished, `{"replyTo":"tweet-123"}`)
	attachedID := insert("attached", now.Add(-time.Hour), false, nil, `{"replyTo":"tweet-123"}`)
	overriddenID := insert("overridden", now.Add(-time.Hour), false, nil, `{"replyTo":"tweet-123"}`)
	judgedID := insert("judged", now.Add(-time.Hour), false, nil, `{"replyTo":"tweet-123"}`)
	if err := db.Exec(`INSERT INTO grouping_decisions(article_id,verdict) VALUES (?,'signal-unmatched'),(?,'signal')`, ids["unmatched"], judgedID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO grouping_overrides(article_id,reason) VALUES (?,'manual')`, overriddenID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO story_signals(story_id,article_id,participant_key,source_id,kind,observed_at)
		VALUES (?,?,'participant',?,'signal',?)`, storyID, attachedID, sourceID, now).Error; err != nil {
		t.Fatal(err)
	}

	waiting, err := repository.WaitingSignals(ctx, "tweet-123", now.Add(-signalWaitWindow))
	if err != nil {
		t.Fatal(err)
	}
	waitingIDs := map[string]bool{}
	for _, row := range waiting {
		waitingIDs[row.ArticleID] = true
	}
	if len(waitingIDs) != 3 || !waitingIDs[replyID] || !waitingIDs[quoteID] || !waitingIDs[ids["unmatched"]] {
		t.Fatalf("reply/quote reclaim eligibility changed: %#v", waitingIDs)
	}
	unattached, err := repository.UnattachedSignals(ctx, now.Add(-signalRevisitWindow))
	if err != nil {
		t.Fatal(err)
	}
	unattachedIDs := map[string]bool{}
	for _, row := range unattached {
		unattachedIDs[row.ArticleID] = true
	}
	want := []string{replyID, quoteID, ids["unmatched"], ids["other-recent"]}
	if len(unattachedIDs) != len(want) {
		t.Fatalf("semantic rematch eligibility changed: %#v", unattachedIDs)
	}
	for _, id := range want {
		if !unattachedIDs[id] {
			t.Fatalf("eligible signal %q missing from rematch pool: %#v", id, unattachedIDs)
		}
	}
}

/** TestEvaluationUsageSumsDistinctReceipts checks token fallback fields and latency averaging. */
func TestEvaluationUsageSumsDistinctReceipts(t *testing.T) {
	db, _ := openGroupingTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec(`CREATE TEMP TABLE receipt_attempts (receipt_id bigint, usage jsonb, latency_ms integer) ON COMMIT DROP`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`INSERT INTO receipt_attempts(receipt_id, usage, latency_ms) VALUES
		(11, '{"prompt_tokens":10,"completion_tokens":4}'::jsonb, 100),
		(11, '{"input_tokens":5,"output_tokens":2}'::jsonb, 101),
		(12, NULL, NULL),
		(13, '{"prompt_tokens":99}'::jsonb, 999)`).Error; err != nil {
		t.Fatal(err)
	}
	usage, err := NewRepository(tx).EvaluationUsage(context.Background(), []int64{11, 11, 12})
	if err != nil || usage != (EvaluationUsage{TokensIn: 15, TokensOut: 6, AvgLatencyMS: 101}) {
		t.Fatalf("receipt usage = %#v, err=%v", usage, err)
	}
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
	selectedID, futureID, unselectedID, withdrawnID, oldID := "article_"+fixture+"_selected", "article_"+fixture+"_future", "article_"+fixture+"_unselected", "article_"+fixture+"_withdrawn", "article_"+fixture+"_old"
	factIDArticle, referencedID, candidateID := "article_"+fixture+"_fact", "article_"+fixture+"_ref", "article_"+fixture+"_candidate"
	articleIDs := []string{selectedID, futureID, unselectedID, withdrawnID, oldID, factIDArticle, referencedID, candidateID}
	var storyID, factID int64
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM publications WHERE article_id IN ?", articleIDs).Error
		_ = db.Exec("DELETE FROM fact_articles WHERE article_id IN ?", articleIDs).Error
		_ = db.Exec("DELETE FROM analyses WHERE article_id IN ?", articleIDs).Error
		_ = db.Exec("DELETE FROM articles WHERE id IN ?", articleIDs).Error
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
	insertArticle(unselectedID, "url:"+unselectedID, "unselected", false, now.Add(-time.Minute), nil)
	insertArticle(withdrawnID, "url:"+withdrawnID, "withdrawn", true, now.Add(-time.Minute), nil)
	insertArticle(oldID, "url:"+oldID, "old", true, now.Add(-time.Minute), nil)
	insertArticle(factIDArticle, "url:"+factIDArticle, "has fact", true, now.Add(-time.Minute), &factID)
	insertArticle(referencedID, "x:tweet-123", "referenced", false, now, nil)
	insertArticle(candidateID, "url:"+candidateID, "candidate", true, now.Add(-time.Minute), &factID)
	if err := db.Exec("UPDATE publications SET visibility='withdrawn' WHERE article_id=?", withdrawnID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE publications SET discovered_at=? WHERE article_id=?", now.Add(-16*24*time.Hour), oldID).Error; err != nil {
		t.Fatal(err)
	}
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

/** TestCandidateViewsUsesChronologicalStoryRoot preserves roots after imported fact IDs diverge from time. */
func TestCandidateViewsUsesChronologicalStoryRoot(t *testing.T) {
	db, repository := openGroupingTestDB(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("grouping_root_%d", time.Now().UnixNano())
	sourceID := "source_" + fixture
	firstArticleID, secondArticleID := "article_"+fixture+"_first", "article_"+fixture+"_second"
	var storyID, emptyStoryID, firstFactID, secondFactID int64
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM articles WHERE id IN ?", []string{firstArticleID, secondArticleID}).Error
		_ = db.Exec("DELETE FROM facts WHERE story_id = ?", storyID).Error
		_ = db.Exec("DELETE FROM stories WHERE id = ?", emptyStoryID).Error
		_ = db.Exec("DELETE FROM stories WHERE id = ?", storyID).Error
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})

	if err := db.Exec("INSERT INTO sources (id,name,kind) VALUES (?, 'Root chronology fixture', 'rss')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	publicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	if err := db.Raw("INSERT INTO stories (public_id,title) VALUES (?,'root chronology fixture') RETURNING id", publicID).Scan(&storyID).Error; err != nil {
		t.Fatal(err)
	}
	emptyPublicID := fmt.Sprintf("00000000-0000-4000-8000-%012x", (time.Now().UnixNano()+1)&0xffffffffffff)
	if err := db.Raw("INSERT INTO stories (public_id,title) VALUES (?,'empty redirect fixture') RETURNING id", emptyPublicID).Scan(&emptyStoryID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("INSERT INTO facts (public_id,story_id,title) VALUES (?,?,'lower fact id') RETURNING id", fixture+"_first", storyID).Scan(&firstFactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("INSERT INTO facts (public_id,story_id,title) VALUES (?,?,'earlier reported fact') RETURNING id", fixture+"_second", storyID).Scan(&secondFactID).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	earlier, later := now.Add(-2*time.Hour), now.Add(-time.Hour)
	for _, article := range []struct {
		id, title string
		factID    int64
		at        time.Time
	}{
		{id: firstArticleID, title: "first fact report", factID: firstFactID, at: later},
		{id: secondArticleID, title: "earlier fact report", factID: secondFactID, at: earlier},
	} {
		articleURL := "https://example.invalid/" + article.id
		if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES(?,?,?,?,?,?,?)`, article.id, sourceID, "url:"+article.id, articleURL, article.title, now, now).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO publications(article_id,title,source_id,channel,url,published_at,discovered_at,timeline_at)
			VALUES(?,?,?,'news',?,?,?,?)`, article.id, article.title, sourceID, articleURL, article.at, now, now).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO fact_articles(fact_id,article_id,role) VALUES (?,?,'report')", article.factID, article.id).Error; err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := repository.CandidateViews(ctx, []Recalled{
		{FactID: firstFactID, StoryID: storyID, Score: 0.9},
		{FactID: secondFactID, StoryID: storyID, Score: 0.8},
	}, nil, now)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidate roots = %#v, err=%v", candidates, err)
	}
	if candidates[0].StoryRoot || !candidates[1].StoryRoot {
		t.Fatalf("story root followed fact ID instead of report chronology: %#v", candidates)
	}
	roots, err := repository.StoryRoots(ctx, []int64{storyID})
	if err != nil || len(roots) != 1 || roots[0].StoryID != storyID || roots[0].At != earlier || roots[0].Report.Title != "earlier fact report" || roots[0].Roundup {
		t.Fatalf("story root comparison input changed: roots=%#v err=%v", roots, err)
	}
	if err := db.Exec(`INSERT INTO grouping_decisions(article_id,story_id,verdict,candidates)
		VALUES (? ,?,'new-story','[]'::jsonb)`, firstArticleID, emptyStoryID).Error; err != nil {
		t.Fatal(err)
	}
	empty, err := repository.EmptyStories(ctx, firstArticleID, storyID)
	if err != nil || len(empty) != 1 || empty[0] != emptyStoryID {
		t.Fatalf("prior empty story candidates = %v, err=%v", empty, err)
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

/** TestEmbeddingRepositoryReusesPaidResponseAfterAtomicSaveRollback protects receipt recovery. */
func TestEmbeddingRepositoryReusesPaidResponseAfterAtomicSaveRollback(t *testing.T) {
	db, repository := openGroupingTestDB(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	modelName := "embedding-retry-model"
	firstID, secondID := "article_retry_a_"+suffix, "article_retry_b_"+suffix
	functionName, triggerName := "test_embedding_rollback_"+suffix, "test_embedding_trigger_"+suffix
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON embeddings", triggerName)).Error
		_ = db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)).Error
		_ = db.Exec("DELETE FROM embeddings WHERE kind = 'article' AND model = ? AND ref_id IN ?", modelName, []string{firstID, secondID}).Error
		_ = db.Exec(`DELETE FROM receipt_attempts WHERE receipt_id IN (
			SELECT id FROM receipts WHERE service = 'embedding' AND purpose = 'embedding' AND subject = ?
		)`, "article:"+firstID).Error
		_ = db.Exec("DELETE FROM receipts WHERE service = 'embedding' AND purpose = 'embedding' AND subject = ?", "article:"+firstID).Error
	})
	if err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.ref_id = '%s' THEN RAISE EXCEPTION 'intentional embedding rollback'; END IF;
			RETURN NEW;
		END $$`, functionName, secondID)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT OR UPDATE ON embeddings FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)).Error; err != nil {
		t.Fatal(err)
	}
	provider := &embeddingProviderStub{result: func([]string) EmbeddingResponse {
		body, _ := json.Marshal(map[string]any{"data": []map[string]any{
			{"index": 1, "embedding": []float32{0, 1}},
			{"index": 0, "embedding": []float32{1, 0}},
		}})
		return EmbeddingResponse{Body: body, RequestID: "fake-embedding-request"}
	}}
	store := NewEmbeddingStore(repository, receipts.NewService(receipts.NewRepository(db)), EmbeddingConfig{
		Enabled: true, Service: "embedding", Model: modelName, APIKey: "fake-key", Dimensions: 2,
	}, provider)
	inputs := []VectorInput{{ID: firstID, Text: "first"}, {ID: secondID, Text: "second"}}
	if _, err := store.Ensure(ctx, inputs); err == nil || !strings.Contains(err.Error(), "intentional embedding rollback") || provider.calls != 1 {
		t.Fatalf("failed batch did not preserve its single paid response: calls=%d err=%v", provider.calls, err)
	}
	var embeddingCount int
	if err := db.Raw("SELECT count(*) FROM embeddings WHERE kind = 'article' AND model = ? AND ref_id IN ?", modelName, []string{firstID, secondID}).Scan(&embeddingCount).Error; err != nil || embeddingCount != 0 {
		t.Fatalf("failed embedding batch left partial rows: count=%d err=%v", embeddingCount, err)
	}
	var status string
	var response []byte
	var completedAt *time.Time
	if err := db.Raw("SELECT status, response, completed_at FROM receipts WHERE service = 'embedding' AND purpose = 'embedding' AND subject = ?", "article:"+firstID).Row().Scan(&status, &response, &completedAt); err != nil || status != "received" || len(response) == 0 || completedAt != nil {
		t.Fatalf("failed batch did not retain a reusable receipt: status=%q response=%s completed=%v err=%v", status, response, completedAt, err)
	}
	if err := db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON embeddings", triggerName)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)).Error; err != nil {
		t.Fatal(err)
	}
	vectors, err := store.Ensure(ctx, inputs)
	if err != nil || provider.calls != 1 || vectors[firstID][0] != 1 || vectors[secondID][0] != 1 {
		t.Fatalf("retry did not reuse the paid response in input order: vectors=%v calls=%d err=%v", vectors, provider.calls, err)
	}
	if err := db.Raw("SELECT status, completed_at FROM receipts WHERE service = 'embedding' AND purpose = 'embedding' AND subject = ?", "article:"+firstID).Row().Scan(&status, &completedAt); err != nil || status != "completed" || completedAt == nil {
		t.Fatalf("successful retry did not complete its receipt: status=%q completed=%v err=%v", status, completedAt, err)
	}
}
