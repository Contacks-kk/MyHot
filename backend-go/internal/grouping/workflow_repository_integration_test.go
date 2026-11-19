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

func TestGroupingWorkflowCommitIsAtomicAndPreservesArticleIdentity(t *testing.T) {
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
	repository := NewRepository(db)
	ctx := context.Background()
	prefix := fmt.Sprintf("grouping_workflow_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	manualRaceArticleID := prefix + "_manual_race_article"
	createdStoryTitle := prefix + " committed story"
	rollbackStoryTitle := prefix + " rolled back story"
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM articles WHERE id IN (?,?,?)`, articleID, manualRaceArticleID, prefix+"_rollback_article").Error
		_ = db.Exec(`DELETE FROM facts WHERE public_id LIKE ?`, prefix+"_%").Error
		_ = db.Exec(`DELETE FROM stories WHERE title IN (?,?)`, createdStoryTitle, rollbackStoryTitle).Error
		_ = db.Exec(`DELETE FROM sources WHERE id=?`, sourceID).Error
	})
	if err := db.Exec(`INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES (?, 'Grouping workflow fixture', 'rss', 'T1', 'editorial')`, sourceID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	insertArticle := func(id string) {
		t.Helper()
		if err := db.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,x_post)
			VALUES (?,?,?,?,?,?,?,'{"tweetId":"123456789","replyTo":"99887766","quoted":{"url":"https://x.com/acme/status/88776655"}}')`,
			id, sourceID, "url:https://example.invalid/"+id, "https://example.invalid/"+id, "Fixture launch", now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertArticle(articleID)
	if err := db.Exec(`INSERT INTO analyses(article_id,input_revision,origin,relevance,title_zh,summary_zh,output)
		VALUES (?,1,'model','pass','中文标题','中文摘要','{"scope":"single","fact":{"title":"核心事实","subject":"Fixture","action":"launched","object":"product","occurredAt":"2026-10-09"}}')`, articleID).Error; err != nil {
		t.Fatal(err)
	}
	article, err := repository.ArticleForGrouping(ctx, articleID)
	if err != nil || article == nil {
		t.Fatalf("article=%+v err=%v", article, err)
	}
	if article.XPost.ReplyTo == nil || *article.XPost.ReplyTo != "99887766" || article.XPost.Quoted == nil {
		t.Fatalf("social identity not decoded: %+v", article.XPost)
	}
	analysis, err := repository.LatestAnalysis(ctx, articleID)
	if err != nil || analysis == nil || analysis.Relevance != "pass" {
		t.Fatalf("analysis=%+v err=%v", analysis, err)
	}
	if analysis.Composite {
		t.Fatal("single material was classified as a composite")
	}
	start, found, err := repository.BeginGrouping(ctx, articleID)
	if err != nil || !found || start.Revision != 1 {
		t.Fatalf("start=%+v found=%v err=%v", start, found, err)
	}
	frame := &Frame{Subject: stringPointer("Fixture"), Action: stringPointer("launched"), Object: stringPointer("product"), OccurredAt: stringPointer("2026-10-09")}
	committed, err := repository.CommitGrouping(ctx, CommitInput{Article: *article, Verdict: "new-story", Selection: &SelectionValue{AddsValue: true, Reason: "新增事实"},
		CreateStory: true, CreateFact: true, AddMembership: true, FactTitle: "核心事实", Frame: frame, FirstParty: true, ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if committed.FactID == nil || committed.StoryID == nil {
		t.Fatalf("commit returned no identity: %+v", committed)
	}
	var persisted struct {
		Role            string `gorm:"column:role"`
		Status          string `gorm:"column:grouping_status"`
		SelectionReason string `gorm:"column:selection_value_reason"`
		SignalCount     int    `gorm:"column:signal_count"`
		DecisionCount   int    `gorm:"column:decision_count"`
	}
	if err := db.Raw(`SELECT fa.role, a.grouping_status, a.selection_value_reason,
		(SELECT count(*)::int FROM story_signals ss WHERE ss.story_id=? AND ss.article_id=a.id) AS signal_count,
		(SELECT count(*)::int FROM grouping_decisions d WHERE d.article_id=a.id AND d.story_id=?) AS decision_count
		FROM articles a JOIN fact_articles fa ON fa.article_id=a.id WHERE a.id=? AND fa.fact_id=?`,
		*committed.StoryID, *committed.StoryID, articleID, *committed.FactID).Scan(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Role != "primary" || persisted.Status != "complete" || persisted.SelectionReason != "新增事实" || persisted.SignalCount != 1 || persisted.DecisionCount != 1 {
		t.Fatalf("persisted grouping differs: %+v", persisted)
	}

	insertArticle(manualRaceArticleID)
	if err := db.Exec(`INSERT INTO grouping_overrides(article_id,reason,actor) VALUES (?,'discussion post detached','test-editor')`, manualRaceArticleID).Error; err != nil {
		t.Fatal(err)
	}
	manualCommit, err := repository.CommitGrouping(ctx, CommitInput{Article: ArticleInput{ID: manualRaceArticleID, Revision: 1, SourceID: sourceID},
		Verdict: "signal", FactID: committed.FactID, StoryID: committed.StoryID, AddMembership: true, Signal: true, ObservedAt: now})
	if err != nil || !manualCommit.Manual.Found {
		t.Fatalf("manual discussion detach lost the grouping race: result=%+v err=%v", manualCommit, err)
	}
	var raceMemberships, raceSignals, raceDecisions int
	if err := db.Raw(`SELECT count(*) FROM fact_articles WHERE article_id=?`, manualRaceArticleID).Scan(&raceMemberships).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM story_signals WHERE article_id=?`, manualRaceArticleID).Scan(&raceSignals).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM grouping_decisions WHERE article_id=?`, manualRaceArticleID).Scan(&raceDecisions).Error; err != nil {
		t.Fatal(err)
	}
	if raceMemberships != 0 || raceSignals != 0 || raceDecisions != 0 {
		t.Fatalf("manual discussion detach was overwritten: memberships=%d signals=%d decisions=%d", raceMemberships, raceSignals, raceDecisions)
	}

	rollbackArticle := prefix + "_rollback_article"
	staleStoryTitle := prefix + " stale story"
	currentStoryTitle := prefix + " current story"
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM stories WHERE title IN (?,?)`, staleStoryTitle, currentStoryTitle).Error
	})
	insertArticle(rollbackArticle)
	stalePublicID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	currentPublicID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	var staleStoryID, currentStoryID int64
	if err := db.Raw(`INSERT INTO stories (public_id,title) VALUES (?,?) RETURNING id`, stalePublicID, staleStoryTitle).Scan(&staleStoryID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO stories (public_id,title) VALUES (?,?) RETURNING id`, currentPublicID, currentStoryTitle).Scan(&currentStoryID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE stories SET merged_into=? WHERE id=?`, currentStoryID, staleStoryID).Error; err != nil {
		t.Fatal(err)
	}
	staleInput := CommitInput{Article: ArticleInput{ID: rollbackArticle, Revision: 1, SourceID: sourceID}, Verdict: "same-fact",
		StoryID: &staleStoryID, ObservedAt: now}
	if _, err := repository.CommitGrouping(ctx, staleInput); err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("merged story target should request a fresh grouping decision: %v", err)
	}
	rollbackInput := CommitInput{Article: ArticleInput{ID: rollbackArticle, Revision: 1, SourceID: sourceID}, Verdict: "same-fact",
		FactID: int64Pointer(9223372036854770000), CreateStory: true, CreateFact: true, AddMembership: true,
		FactTitle: rollbackStoryTitle, ObservedAt: now}
	if _, err := repository.CommitGrouping(ctx, rollbackInput); err == nil {
		t.Fatal("expected invalid fact reference to roll the grouping transaction back")
	}
	var rolledBack int64
	if err := db.Raw(`SELECT count(*) FROM stories WHERE title=?`, rollbackStoryTitle).Scan(&rolledBack).Error; err != nil {
		t.Fatal(err)
	}
	if rolledBack != 0 {
		t.Fatalf("partial story write survived rollback: %d", rolledBack)
	}
}

func int64Pointer(value int64) *int64 { return &value }
