package admincontent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/contentpipeline"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/jobargs"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/publication"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testXEncodingEffects struct{}

/** EnqueueSelectedTx keeps this fixture's projection free of external job effects. */
func (testXEncodingEffects) EnqueueSelectedTx(context.Context, pgx.Tx, string, time.Time) error {
	return nil
}

/** EnqueueMediaTx keeps this fixture's projection free of external media effects. */
func (testXEncodingEffects) EnqueueMediaTx(context.Context, pgx.Tx, string) error {
	return nil
}

/** ArticleChangedTx keeps this fixture's projection free of external digest effects. */
func (testXEncodingEffects) ArticleChangedTx(context.Context, pgx.Tx, publication.ArticleChanged) error {
	return nil
}

func reportCorrectionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end with _test or _ci")
	}
	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestReportClassificationCorrectionMovesFrozenEntriesAndArchivesRevision(t *testing.T) {
	pool := reportCorrectionPool(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_report_%d", time.Now().UnixNano())
	sourceID, articleID := "source_"+fixture, "article_"+fixture
	dailyKey, weeklyKey, monthlyKey := fixture+"-daily", fixture+"-weekly", fixture+"-monthly"
	t.Cleanup(func() {
		keys := []string{dailyKey, weeklyKey, monthlyKey}
		_, _ = pool.Exec(ctx, `DELETE FROM report_revisions WHERE report_id IN (SELECT id FROM reports WHERE key=ANY($1))`, keys)
		_, _ = pool.Exec(ctx, `DELETE FROM reports WHERE key=ANY($1)`, keys)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	articleURL := "https://example.invalid/" + fixture
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind) VALUES($1,'Report correction fixture','external')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES($1,$2,$3,$4,'Report correction fixture',now(),now())`, articleID, sourceID, "url:"+articleURL, articleURL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO publications(article_id,title,summary,category,tags,source_id,channel,url,discovered_at,timeline_at)
		VALUES($1,'Report correction fixture','summary','ai-models',ARRAY['new'], $2,'news',$3,now(),now())`, articleID, sourceID, articleURL); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"itemId": articleID, "firstParty": true, "role": "官方"}
	daily, _ := json.Marshal(map[string]any{"leadItemId": articleID, "lead": map[string]any{"title": "Frozen lead"}, "highlights": []string{articleID}, "sections": []any{map[string]any{"label": "Industry", "items": []any{entry}}}, "metrics": map[string]any{"modelsReleased": 0}})
	weekly, _ := json.Marshal(map[string]any{"headline": "Frozen headline", "leadItemId": articleID, "storyOrder": []string{articleID}, "overview": "Preserve overview", "themes": []any{map[string]any{"heading": "Industry", "summary": "old", "storyRefs": []any{entry}}}})
	monthly, _ := json.Marshal(map[string]any{"headline": "Frozen headline", "leadItemId": articleID, "storyOrder": []string{articleID}, "overview": "Preserve overview", "themes": []any{map[string]any{"heading": "Industry", "summary": "old", "storyRefs": []any{entry}}}})
	start, end := time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)
	for _, row := range []struct {
		kind, key string
		content   []byte
	}{{"daily", dailyKey, daily}, {"weekly", weeklyKey, weekly}, {"monthly", monthlyKey, monthly}} {
		if _, err := pool.Exec(ctx, `INSERT INTO reports(kind,key,window_start,window_end,content,generated_at) VALUES($1,$2,$3,$4,$5::jsonb,now())`, row.kind, row.key, start, end, row.content); err != nil {
			t.Fatal(err)
		}
	}
	repository := &CommandsRepository{reportCorrection: ReportCorrectionConfig{
		SectionByCategory: map[string]string{"ai-models": "Model", "industry": "Industry"},
		SectionOrder:      []string{"Industry", "Model"}, ReleaseCategory: "ai-models", ReleaseTag: "new",
	}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.correctReportClassification(ctx, tx, articleID, "verified category"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var dailyContent, weeklyContent, monthlyContent json.RawMessage
	var dailyRevision, weeklyRevision, monthlyRevision int
	if err := pool.QueryRow(ctx, `SELECT content,revision FROM reports WHERE key=$1`, dailyKey).Scan(&dailyContent, &dailyRevision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT content,revision FROM reports WHERE key=$1`, weeklyKey).Scan(&weeklyContent, &weeklyRevision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT content,revision FROM reports WHERE key=$1`, monthlyKey).Scan(&monthlyContent, &monthlyRevision); err != nil {
		t.Fatal(err)
	}
	var d, w, m map[string]any
	_ = json.Unmarshal(dailyContent, &d)
	_ = json.Unmarshal(weeklyContent, &w)
	_ = json.Unmarshal(monthlyContent, &m)
	dSections := reportGroups(d["sections"])
	wThemes := reportGroups(w["themes"])
	mThemes := reportGroups(m["themes"])
	if dailyRevision != 2 || len(dSections) != 1 || dSections[0]["label"] != "Model" || reportEntries(dSections[0]["items"], articleID) != 1 || d["metrics"].(map[string]any)["modelsReleased"] != float64(1) {
		t.Fatalf("daily frozen entry was not moved/recounted: revision=%d content=%s", dailyRevision, dailyContent)
	}
	if weeklyRevision != 2 || len(wThemes) != 1 || wThemes[0]["heading"] != "Model" || reportEntries(wThemes[0]["storyRefs"], articleID) != 1 || wThemes[0]["summary"] != nil || w["overview"] != "Preserve overview" || w["leadItemId"] != articleID {
		t.Fatalf("weekly frozen reference was not moved/reset: revision=%d content=%s", weeklyRevision, weeklyContent)
	}
	if monthlyRevision != 2 || len(mThemes) != 1 || mThemes[0]["heading"] != "Model" || reportEntries(mThemes[0]["storyRefs"], articleID) != 1 || mThemes[0]["summary"] != nil || m["overview"] != "Preserve overview" || m["leadItemId"] != articleID {
		t.Fatalf("monthly frozen reference was not moved/reset: revision=%d content=%s", monthlyRevision, monthlyContent)
	}
	if d["leadItemId"] != articleID || d["lead"].(map[string]any)["title"] != "Frozen lead" || len(d["highlights"].([]any)) != 1 {
		t.Fatalf("daily lead and highlights changed: content=%s", dailyContent)
	}
	var archived int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_revisions rr JOIN reports r ON r.id=rr.report_id WHERE r.key=ANY($1)`, []string{dailyKey, weeklyKey, monthlyKey}).Scan(&archived); err != nil || archived != 3 {
		t.Fatalf("old report contents were not archived: revisions=%d err=%v", archived, err)
	}
}

/** TestClassificationOnlyOverrideDoesNotEnqueueStoryDigest preserves the no-model correction path. */
func TestClassificationOnlyOverrideDoesNotEnqueueStoryDigest(t *testing.T) {
	pool := reportCorrectionPool(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_classification_%d", time.Now().UnixNano())
	sourceID, articleID := "source_"+fixture, "article_"+fixture
	actor := "admin_" + fixture
	articleURL := "https://example.invalid/" + fixture
	storyPublicID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%999_999_999_999)
	reportKeys := []string{fixture + "-daily", fixture + "-weekly", fixture + "-monthly"}
	functionName := fmt.Sprintf("reject_classification_audit_%d", time.Now().UnixNano())
	triggerName := functionName + "_trigger"
	var storyID int64
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP TRIGGER IF EXISTS "+triggerName+" ON audit_log")
		_, _ = pool.Exec(ctx, "DROP FUNCTION IF EXISTS "+functionName+"()")
		_, _ = pool.Exec(ctx, `DELETE FROM river_job WHERE kind='myhot_story_digest' AND args->>'storyId'=$1`, strconv.FormatInt(storyID, 10))
		_, _ = pool.Exec(ctx, `DELETE FROM audit_log WHERE actor=$1`, actor)
		_, _ = pool.Exec(ctx, `DELETE FROM report_revisions WHERE report_id IN (SELECT id FROM reports WHERE key=ANY($1))`, reportKeys)
		_, _ = pool.Exec(ctx, `DELETE FROM reports WHERE key=ANY($1)`, reportKeys)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM pool_search WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM editorial_overrides WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM fact_articles WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM facts WHERE public_id=$1`, fixture+"_fact")
		_, _ = pool.Exec(ctx, `DELETE FROM stories WHERE id=$1`, storyID)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES($1,'Classification fixture','rss','T1','editorial')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,language,published_at,discovered_at,timeline_at,body_status,body_text,grouping_status)
		VALUES($1,$2,$3,$4,'开源推理工具','zh',now(),now(),now(),'ok','fixture body','complete')`, articleID, sourceID, "url:"+articleURL, articleURL); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO stories(public_id,title,first_report_at,latest_at) VALUES($1,'工具事件',now(),now()) RETURNING id`, storyPublicID).Scan(&storyID); err != nil {
		t.Fatal(err)
	}
	var factID int64
	if err := pool.QueryRow(ctx, `INSERT INTO facts(public_id,title,story_id) VALUES($1,'工具发布',$2) RETURNING id`, fixture+"_fact", storyID).Scan(&factID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO fact_articles(fact_id,article_id,role) VALUES($1,$2,'report')`, factID, articleID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analyses(article_id,input_revision,origin,relevance,category,tags,title_zh,summary_zh,score,selected,output)
		VALUES($1,1,'model','pass','ai-models',ARRAY['old'],'开源推理工具','模型摘要',88,true,'{"scope":"single"}'::jsonb)`, articleID); err != nil {
		t.Fatal(err)
	}
	publisher := publication.NewRepository(pool, nil, interests.PublicationSettings{}, "Fixture", "https://example.invalid")
	publisher.SetEffects(testXEncodingEffects{})
	if result, err := publisher.Publish(ctx, articleID, publication.PublishOptions{}); err != nil || result == nil || !result.Selected {
		t.Fatalf("fixture publication=%#v err=%v", result, err)
	}
	entry := map[string]any{"itemId": articleID, "title": "Frozen title", "summary": "Frozen summary", "firstParty": true, "role": "官方"}
	dailyContent, _ := json.Marshal(map[string]any{
		"leadItemId": articleID, "lead": map[string]any{"title": "Frozen lead"}, "highlights": []string{articleID},
		"sections": []any{map[string]any{"label": "Model", "items": []any{entry}}},
		"metrics":  map[string]any{"totalEvents": 1, "modelsReleased": 1},
	})
	periodContent, _ := json.Marshal(map[string]any{
		"headline": "Frozen headline", "leadItemId": articleID, "storyOrder": []string{articleID}, "overview": "Preserve overview",
		"themes":  []any{map[string]any{"heading": "Model", "summary": "Old introduction", "storyRefs": []any{entry}}},
		"metrics": map[string]any{"totalStories": 1},
	})
	windowStart, windowEnd := time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)
	for _, report := range []struct {
		kind    string
		key     string
		content []byte
	}{
		{kind: "daily", key: reportKeys[0], content: dailyContent},
		{kind: "weekly", key: reportKeys[1], content: periodContent},
		{kind: "monthly", key: reportKeys[2], content: periodContent},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO reports(kind,key,window_start,window_end,content,generated_at,origin)
			VALUES($1,$2,$3,$4,$5::jsonb,now(),'imported')`, report.kind, report.key, windowStart, windowEnd, report.content); err != nil {
			t.Fatal(err)
		}
	}
	type selectionSnapshot struct {
		selected, seat                bool
		score                         sql.NullFloat64
		visibleAfter, selectedReadyAt sql.NullTime
	}
	readSelection := func() selectionSnapshot {
		t.Helper()
		var snapshot selectionSnapshot
		if err := pool.QueryRow(ctx, `SELECT selected,seat,score,visible_after,selected_ready_at FROM publications WHERE article_id=$1`, articleID).
			Scan(&snapshot.selected, &snapshot.seat, &snapshot.score, &snapshot.visibleAfter, &snapshot.selectedReadyAt); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	before := readSelection()
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	commands := NewCommandsRepository(pool, publisher, client, true, ReportCorrectionConfig{
		SectionByCategory: map[string]string{"ai-models": "Model", "ai-products": "Product", "industry": "Industry"},
		SectionOrder:      []string{"Industry", "Model", "Product"},
		ReleaseCategory:   "ai-models",
		ReleaseTag:        "old",
	})
	change := OverrideInput{
		Fields: map[string]json.RawMessage{
			"category": json.RawMessage(`"ai-products"`),
			"tags":     json.RawMessage(`["new"]`),
		},
		Reason:  "verified classification",
		Version: 0,
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'category audit rejected'; END; $$`, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := commands.OverrideFields(ctx, articleID, change, actor); !found || err == nil || !strings.Contains(err.Error(), "category audit rejected") {
		t.Fatalf("classification audit failure found=%t err=%v", found, err)
	}
	var category string
	if err := pool.QueryRow(ctx, `SELECT category FROM publications WHERE article_id=$1`, articleID).Scan(&category); err != nil {
		t.Fatal(err)
	}
	var revisions, overrides int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_revisions WHERE report_id IN (SELECT id FROM reports WHERE key=ANY($1))`, reportKeys).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM editorial_overrides WHERE article_id=$1`, articleID).Scan(&overrides); err != nil {
		t.Fatal(err)
	}
	if category != "ai-models" || revisions != 0 || overrides != 0 {
		t.Fatalf("failed classification audit escaped transaction: category=%q revisions=%d overrides=%d", category, revisions, overrides)
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER "+triggerName+" ON audit_log"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DROP FUNCTION "+functionName+"()"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := commands.OverrideFields(ctx, articleID, change, actor); err != nil || !found {
		t.Fatalf("classification correction found=%t err=%v", found, err)
	}
	after := readSelection()
	if before != after {
		t.Fatalf("classification correction changed selection state: before=%+v after=%+v", before, after)
	}
	readReport := func(key string) (map[string]any, int) {
		t.Helper()
		var raw json.RawMessage
		var revision int
		if err := pool.QueryRow(ctx, `SELECT content,revision FROM reports WHERE key=$1`, key).Scan(&raw, &revision); err != nil {
			t.Fatal(err)
		}
		content := map[string]any{}
		if err := json.Unmarshal(raw, &content); err != nil {
			t.Fatal(err)
		}
		return content, revision
	}
	for index, kind := range []string{"daily", "weekly", "monthly"} {
		content, revision := readReport(reportKeys[index])
		if revision != 2 || content["leadItemId"] != articleID {
			t.Fatalf("%s report correction revision=%d lead=%v", kind, revision, content["leadItemId"])
		}
		if kind == "daily" {
			sections := reportGroups(content["sections"])
			metrics, _ := content["metrics"].(map[string]any)
			lead, _ := content["lead"].(map[string]any)
			if len(sections) != 1 || sections[0]["label"] != "Product" || metrics["modelsReleased"] != float64(0) || lead["title"] != "Frozen lead" || len(content["highlights"].([]any)) != 1 {
				t.Fatalf("daily category correction changed unrelated report content: %v", content)
			}
			continue
		}
		themes := reportGroups(content["themes"])
		if len(themes) != 1 || themes[0]["heading"] != "Product" || themes[0]["summary"] != nil || content["overview"] != "Preserve overview" || len(content["storyOrder"].([]any)) != 1 {
			t.Fatalf("%s category correction changed unrelated report content: %v", kind, content)
		}
	}
	var archived int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_revisions rr JOIN reports r ON r.id=rr.report_id WHERE r.key=ANY($1)`, reportKeys).Scan(&archived); err != nil || archived != 3 {
		t.Fatalf("classification revisions were not archived: revisions=%d err=%v", archived, err)
	}
	readDigestJobs := func() int {
		t.Helper()
		var digestJobs int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='myhot_story_digest' AND args->>'storyId'=$1`, strconv.FormatInt(storyID, 10)).Scan(&digestJobs); err != nil {
			t.Fatal(err)
		}
		return digestJobs
	}
	if digestJobs := readDigestJobs(); digestJobs != 0 {
		t.Fatalf("classification-only correction queued %d story-digest jobs", digestJobs)
	}
	if _, found, err := commands.OverrideFields(ctx, articleID, OverrideInput{
		Clear:   []string{"category", "tags"},
		Reason:  "restore classification",
		Version: 1,
	}, actor); err != nil || !found {
		t.Fatalf("classification restore found=%t err=%v", found, err)
	}
	restored, restoredRevision := readReport(reportKeys[0])
	restoredSections := reportGroups(restored["sections"])
	restoredMetrics, _ := restored["metrics"].(map[string]any)
	if restoredRevision != 3 || len(restoredSections) != 1 || restoredSections[0]["label"] != "Model" || restoredMetrics["modelsReleased"] != float64(1) {
		t.Fatalf("restoring classification did not restore daily report: revision=%d content=%v", restoredRevision, restored)
	}
	if afterRestore := readSelection(); before != afterRestore {
		t.Fatalf("restoring classification changed selection state: before=%+v after=%+v", before, afterRestore)
	}
	if digestJobs := readDigestJobs(); digestJobs != 0 {
		t.Fatalf("classification restore queued %d story-digest jobs", digestJobs)
	}
	if _, found, err := commands.OverrideFields(ctx, articleID, OverrideInput{
		Fields:  map[string]json.RawMessage{"title": json.RawMessage(`"Verified title"`)},
		Reason:  "verified title",
		Version: 2,
	}, actor); err != nil || !found {
		t.Fatalf("content correction found=%t err=%v", found, err)
	}
	if digestJobs := readDigestJobs(); digestJobs != 1 {
		t.Fatalf("content correction queued %d story-digest jobs, want 1", digestJobs)
	}
}

/** TestContentCommandsRollbackWhenAuditInsertFails keeps editor changes and their public projection atomic. */
func TestContentCommandsRollbackWhenAuditInsertFails(t *testing.T) {
	pool := reportCorrectionPool(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_command_rollback_%d", time.Now().UnixNano())
	sourceID, articleID := "source_"+fixture, "article_"+fixture
	actor := "admin_" + fixture
	articleURL := "https://example.invalid/" + fixture
	functionName := fmt.Sprintf("refuse_content_audit_%d", time.Now().UnixNano())
	triggerName := functionName + "_trigger"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP TRIGGER IF EXISTS "+triggerName+" ON audit_log")
		_, _ = pool.Exec(ctx, "DROP FUNCTION IF EXISTS "+functionName+"()")
		_, _ = pool.Exec(ctx, `DELETE FROM audit_log WHERE actor=$1`, actor)
		_, _ = pool.Exec(ctx, `DELETE FROM editorial_overrides WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES($1,'Command fixture','external','T1','editorial')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
		VALUES($1,$2,$3,$4,'Original title',now(),now())`, articleID, sourceID, "url:"+articleURL, articleURL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO publications(article_id,title,summary,source_id,channel,url,discovered_at,timeline_at)
		VALUES($1,'Original title','Original summary',$2,'news',$3,now(),now())`, articleID, sourceID, articleURL); err != nil {
		t.Fatal(err)
	}
	publisher := publication.NewRepository(pool, nil, interests.PublicationSettings{}, "Fixture", "https://example.invalid")
	publisher.SetEffects(testXEncodingEffects{})
	if _, err := publisher.Publish(ctx, articleID, publication.PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	commands := NewCommandsRepository(pool, publisher, client, false, ReportCorrectionConfig{})
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'audit unavailable'; END; $$`, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)); err != nil {
		t.Fatal(err)
	}

	_, found, err := commands.OverrideFields(ctx, articleID, OverrideInput{
		Fields:  map[string]json.RawMessage{"title": json.RawMessage(`"Corrected title"`)},
		Reason:  "verified correction",
		Version: 0,
	}, actor)
	if !found || err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("override audit failure found=%t err=%v", found, err)
	}
	_, found, err = commands.SetVisibility(ctx, articleID, VisibilityInput{
		Visibility: "withdrawn",
		Reason:     "verified withdrawal",
		Version:    0,
	}, actor)
	if !found || err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("visibility audit failure found=%t err=%v", found, err)
	}
	var overrides, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM editorial_overrides WHERE article_id=$1`, articleID).Scan(&overrides); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE actor=$1`, actor).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	var title, visibility string
	if err := pool.QueryRow(ctx, `SELECT title,visibility FROM publications WHERE article_id=$1`, articleID).Scan(&title, &visibility); err != nil {
		t.Fatal(err)
	}
	if overrides != 0 || audits != 0 || title != "Original title" || visibility != "public" {
		t.Fatalf("failed correction escaped its transaction: overrides=%d audits=%d title=%q visibility=%q", overrides, audits, title, visibility)
	}
}

/** TestResetGroupingWithdrawsAutomaticIdentityButKeepsManualMembership locks explicit regroup semantics. */
func TestResetGroupingWithdrawsAutomaticIdentityButKeepsManualMembership(t *testing.T) {
	pool := reportCorrectionPool(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_regroup_%d", time.Now().UnixNano())
	sourceID := "source_" + fixture
	automaticArticleID, manualArticleID := "article_auto_"+fixture, "article_manual_"+fixture
	signalArticleID := "article_signal_" + fixture
	storySeed := time.Now().UnixNano() % 999_999_999_998
	automaticStoryID := fmt.Sprintf("00000000-0000-4000-8000-%012d", storySeed)
	manualStoryID := fmt.Sprintf("00000000-0000-4000-8000-%012d", storySeed+1)
	automaticFactID, manualFactID := fixture+"_auto_fact", fixture+"_manual_fact"
	articleIDs := []string{automaticArticleID, manualArticleID, signalArticleID}
	storyIDs := []string{automaticStoryID, manualStoryID}
	factIDs := []string{automaticFactID, manualFactID}
	actor := "admin_" + fixture
	contentArticleIDs := []string{automaticArticleID, manualArticleID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_log WHERE actor=$1`, actor)
		_, _ = pool.Exec(ctx, `DELETE FROM river_job WHERE kind='events.group' AND args->>'articleId'=$1`, automaticArticleID)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM pool_search WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM grouping_overrides WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM story_signals WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM fact_articles WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM analyses WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM facts WHERE public_id=ANY($1)`, factIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM stories WHERE public_id=ANY($1::uuid[])`, storyIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES($1,'Regroup fixture','rss','T1','editorial')`, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, storyID := range storyIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO stories(public_id,title,first_report_at,latest_at) VALUES($1,$1,now(),now())`, storyID); err != nil {
			t.Fatal(err)
		}
	}
	var automaticFactPK, manualFactPK, automaticStoryPK, manualStoryPK int64
	if err := pool.QueryRow(ctx, `SELECT id FROM stories WHERE public_id=$1`, automaticStoryID).Scan(&automaticStoryPK); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM stories WHERE public_id=$1`, manualStoryID).Scan(&manualStoryPK); err != nil {
		t.Fatal(err)
	}
	factPKByPublicID := make(map[string]int64, len(factIDs))
	for _, row := range []struct {
		publicID string
		storyID  int64
	}{
		{publicID: automaticFactID, storyID: automaticStoryPK},
		{publicID: manualFactID, storyID: manualStoryPK},
	} {
		var factPK int64
		if err := pool.QueryRow(ctx, `INSERT INTO facts(public_id,story_id,title) VALUES($1,$2,$1) RETURNING id`, row.publicID, row.storyID).Scan(&factPK); err != nil {
			t.Fatal(err)
		}
		factPKByPublicID[row.publicID] = factPK
	}
	automaticFactPK, manualFactPK = factPKByPublicID[automaticFactID], factPKByPublicID[manualFactID]
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, row := range []struct {
		articleID string
		storyID   int64
		factID    int64
		manual    bool
	}{
		{articleID: automaticArticleID, storyID: automaticStoryPK, factID: automaticFactPK},
		{articleID: manualArticleID, storyID: manualStoryPK, factID: manualFactPK, manual: true},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,grouping_status,grouped_at,selection_adds_value)
			VALUES($1,$2,$1,$3,$1,$4,$4,'complete',$4,true)`, row.articleID, sourceID, "https://fixture.invalid/"+row.articleID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO analyses(article_id,input_revision,origin,relevance,category,title_zh,summary_zh,score,selected)
			VALUES($1,1,'model','pass','ai-model','标题','摘要',90,true)`, row.articleID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO fact_articles(fact_id,article_id,role,manual) VALUES($1,$2,'primary',$3)`, row.factID, row.articleID, row.manual); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO story_signals(story_id,article_id,participant_key,source_id,kind,observed_at)
			VALUES($1,$2,$3,$4,'editorial',$5)`, row.storyID, row.articleID, "source:"+sourceID, sourceID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO grouping_overrides(article_id,reason) VALUES($1,'standalone')`, row.articleID); err != nil {
			t.Fatal(err)
		}
	}
	publisher := publication.NewRepository(pool, nil, interests.PublicationSettings{}, "Example", "https://example.test")
	publisher.SetEffects(testXEncodingEffects{})
	for _, articleID := range contentArticleIDs {
		if _, err := publisher.Publish(ctx, articleID, publication.PublishOptions{Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	commands := NewCommandsRepository(pool, publisher, client, false, ReportCorrectionConfig{})
	reset := func(articleID string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := commands.resetGrouping(ctx, tx, articleID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	requestID := "regroup_" + fixture
	firstRerun, found, err := commands.Rerun(ctx, automaticArticleID, "group", requestID, actor)
	if err != nil || !found || firstRerun == nil || firstRerun.JobID == nil {
		t.Fatalf("first explicit regroup result=%#v found=%t err=%v", firstRerun, found, err)
	}
	secondRerun, found, err := commands.Rerun(ctx, automaticArticleID, "group", requestID, actor)
	if err != nil || !found || secondRerun == nil || secondRerun.JobID == nil || *secondRerun.JobID != *firstRerun.JobID {
		t.Fatalf("duplicate regroup changed its queued job: first=%#v second=%#v found=%t err=%v", firstRerun, secondRerun, found, err)
	}
	var regroupJobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='events.group' AND args->>'articleId'=$1`, automaticArticleID).Scan(&regroupJobs); err != nil {
		t.Fatal(err)
	}
	if regroupJobs != 1 {
		t.Fatalf("explicit regroup queued %d event-group jobs, want one", regroupJobs)
	}
	reset(manualArticleID)

	var automaticMemberships, automaticSignals, manualMemberships, manualSignals int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fact_articles WHERE article_id=$1`, automaticArticleID).Scan(&automaticMemberships); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM story_signals WHERE article_id=$1`, automaticArticleID).Scan(&automaticSignals); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fact_articles WHERE article_id=$1 AND manual`, manualArticleID).Scan(&manualMemberships); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM story_signals WHERE article_id=$1`, manualArticleID).Scan(&manualSignals); err != nil {
		t.Fatal(err)
	}
	if automaticMemberships != 0 || automaticSignals != 0 || manualMemberships != 1 || manualSignals != 1 {
		t.Fatalf("regroup changed wrong identity state: automatic=(memberships %d signals %d) manual=(memberships %d signals %d)", automaticMemberships, automaticSignals, manualMemberships, manualSignals)
	}
	for _, articleID := range contentArticleIDs {
		var status string
		var groupedAt *time.Time
		var selectionAddsValue *bool
		var selectionReason sql.NullString
		var overrideCount int
		if err := pool.QueryRow(ctx, `SELECT grouping_status,grouped_at,selection_adds_value,selection_value_reason FROM articles WHERE id=$1`, articleID).Scan(&status, &groupedAt, &selectionAddsValue, &selectionReason); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM grouping_overrides WHERE article_id=$1`, articleID).Scan(&overrideCount); err != nil {
			t.Fatal(err)
		}
		if status != "pending" || groupedAt != nil || selectionAddsValue != nil || selectionReason.Valid || overrideCount != 0 {
			t.Fatalf("regroup did not reset decision state for %s: status=%q grouped_at=%v selection=%v reason=%v overrides=%d", articleID, status, groupedAt, selectionAddsValue, selectionReason, overrideCount)
		}
		var factID, storyID sql.NullInt64
		var selected bool
		if err := pool.QueryRow(ctx, `SELECT fact_id,story_id,selected FROM publications WHERE article_id=$1`, articleID).Scan(&factID, &storyID, &selected); err != nil {
			t.Fatal(err)
		}
		if selected {
			t.Fatalf("regroup kept %s in selected publication state", articleID)
		}
		if articleID == automaticArticleID && (factID.Valid || storyID.Valid) {
			t.Fatalf("automatic identity remained public after regroup: fact=%v story=%v", factID, storyID)
		}
		if articleID == manualArticleID && (!factID.Valid || !storyID.Valid) {
			t.Fatalf("manual identity was removed by regroup: fact=%v story=%v", factID, storyID)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,grouping_status)
		VALUES($1,$2,$1,$3,'Discussion reply',$4,$4,'complete')`, signalArticleID, sourceID, "https://fixture.invalid/"+signalArticleID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO story_signals(story_id,article_id,participant_key,source_id,kind,observed_at)
		VALUES($1,$2,$3,$4,'signal',$5)`, automaticStoryPK, signalArticleID, "signal:"+signalArticleID, sourceID, now); err != nil {
		t.Fatal(err)
	}
	detached, found, err := commands.Detach(ctx, signalArticleID, "unrelated discussion", actor)
	if err != nil || !found || detached["detached"] != 0 {
		t.Fatalf("signal-only detach result=%v found=%t err=%v", detached, found, err)
	}
	var remainingSignals, manualOverride int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM story_signals WHERE article_id=$1`, signalArticleID).Scan(&remainingSignals); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM grouping_overrides WHERE article_id=$1`, signalArticleID).Scan(&manualOverride); err != nil {
		t.Fatal(err)
	}
	if remainingSignals != 0 || manualOverride != 1 {
		t.Fatalf("signal-only detach retained story evidence or lost its manual decision: signals=%d override=%d", remainingSignals, manualOverride)
	}
}

/** TestNormalizeXEncodingCommitsAuditedRepairIdempotentlyAndRejectsStaleApproval verifies the transactional operation. */
func TestNormalizeXEncodingCommitsAuditedRepairIdempotentlyAndRejectsStaleApproval(t *testing.T) {
	pool := reportCorrectionPool(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_x_encoding_%d", time.Now().UnixNano())
	sourceID, articleID := "source_"+fixture, "article_"+fixture
	actor, requestID := "operator_"+fixture, "repair_"+fixture
	functionName, triggerName := "refuse_"+fixture, "refuse_"+fixture
	articleURL := "https://x.com/example/status/123456789"
	post := `{"tweetId":"123456789","text":"Research &amp; development\nLiteral &amp;lt; and <code>\nhttps://example.test/?value=&amp;keep=1","quoted":{"handle":"quoted","text":"value &lt; 3 &amp; value &gt; 0","url":"https://x.com/quoted/status/123"}}`
	body := "Research &amp; development\nLiteral &amp;lt; and <code>\nhttps://example.test/?value=&amp;keep=1\n\n【引用 @quoted】value &lt; 3 &amp; value &gt; 0"
	title := "Research &amp; development"
	hash := materials.ContentHash(title, body, "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON audit_log", triggerName))
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
		_, _ = pool.Exec(ctx, `DELETE FROM audit_log WHERE subject=$1`, "content:"+articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM pool_search WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM analyses WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM article_revisions WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,participation_mode,site_fulltext) VALUES($1,'Encoding repair fixture','x_search','editorial',true)`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,language,published_at,body_text,content_hash,x_post,x_article,body_status,grouping_status,selection_adds_value,discovered_at,timeline_at)
		VALUES($1,$2,'x:123456789',$3,$4,'en',now(),$5,$6,$7::jsonb,NULL,'ok','complete',true,now(),now())`, articleID, sourceID, articleURL, title, body, hash, post); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analyses(article_id,input_revision,origin,relevance,category,tags,subjects,title_zh,summary_zh,reason_zh,score,selected,output)
		VALUES($1,1,'model','pass','ai-model',ARRAY['model'],ARRAY['example'],'Research and development','Summary','Reason',8.5,true,'{"scope":"single"}'::jsonb)`, articleID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO article_revisions(article_id,revision,title,body_text,content_hash) VALUES($1,1,$2,$3,$4)`, articleID, title, body, hash); err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	settings := interests.PublicationSettings{PublicCategories: interests.PublicCategories{Merge: map[string]string{"ai-model": "technology"}}}
	publisher := publication.NewRepository(pool, nil, settings, "Fixture", "https://example.invalid")
	publisher.SetEffects(testXEncodingEffects{})
	if published, err := publisher.Publish(ctx, articleID, publication.PublishOptions{}); err != nil || published == nil || !published.Selected {
		t.Fatalf("fixture publication=%#v err=%v", published, err)
	}
	repository := NewCommandsRepository(pool, publisher, client, false, ReportCorrectionConfig{})
	service := NewService(nil)
	service.SetXEncodingRepairRepository(repository)
	plan, err := service.PreviewXEncodingRepair(ctx, articleID)
	if err != nil || plan == nil || !plan.Changed {
		t.Fatalf("preview plan=%#v err=%v", plan, err)
	}
	stale := XEncodingRepairInput{
		Version:   plan.Version + 1,
		Hash:      plan.Hash,
		RequestID: requestID,
		Reason:    "confirmed provider escaping",
	}
	if _, found, err := service.NormalizeXEncoding(ctx, articleID, stale, actor); !found || !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("stale approval found=%t err=%v", found, err)
	}
	input := XEncodingRepairInput{
		Version:   plan.Version,
		Hash:      plan.Hash,
		RequestID: requestID,
		Reason:    "confirmed provider escaping",
	}
	var originalSearch string
	if err := pool.QueryRow(ctx, `SELECT body FROM pool_search WHERE article_id=$1`, articleID).Scan(&originalSearch); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END; $$`, functionName, xEncodingRepairAction)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.NormalizeXEncoding(ctx, articleID, input, actor); err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("audit failure did not stop repair: %v", err)
	}
	var rolledBackTitle, rolledBackBody, rolledBackPublicTitle, rolledBackSearch string
	if err := pool.QueryRow(ctx, `SELECT title,body_text FROM articles WHERE id=$1`, articleID).Scan(&rolledBackTitle, &rolledBackBody); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT original_title FROM publications WHERE article_id=$1`, articleID).Scan(&rolledBackPublicTitle); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT body FROM pool_search WHERE article_id=$1`, articleID).Scan(&rolledBackSearch); err != nil {
		t.Fatal(err)
	}
	if rolledBackTitle != title || rolledBackBody != body || rolledBackPublicTitle != title || rolledBackSearch != originalSearch {
		t.Fatalf("repair survived an audit failure: article=%q/%q publication=%q search=%q", rolledBackTitle, rolledBackBody, rolledBackPublicTitle, rolledBackSearch)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER %s ON audit_log", triggerName)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP FUNCTION %s()", functionName)); err != nil {
		t.Fatal(err)
	}
	result, found, err := service.NormalizeXEncoding(ctx, articleID, input, actor)
	if err != nil || !found || result == nil || result.Status != "repaired" || result.Revision != 1 {
		t.Fatalf("repair result=%#v found=%t err=%v", result, found, err)
	}
	var savedTitle string
	var savedBody string
	var savedPost []byte
	if err := pool.QueryRow(ctx, `SELECT title,body_text,x_post FROM articles WHERE id=$1`, articleID).Scan(&savedTitle, &savedBody, &savedPost); err != nil {
		t.Fatal(err)
	}
	postContainsDecodedText := strings.Contains(string(savedPost), `"text":"Research & development\nLiteral &lt; and <code>`)
	if savedTitle != "Research & development" || !strings.Contains(savedBody, "Literal &lt; and <code>") || !strings.Contains(savedBody, "?value=&amp;keep=1") || !postContainsDecodedText {
		t.Fatalf("stored repair changed nonprovider bytes or missed decoding: title=%q body=%q post=%s", savedTitle, savedBody, savedPost)
	}
	var revisionTitle, revisionBody, revisionHash string
	if err := pool.QueryRow(ctx, `SELECT title,body_text,content_hash FROM article_revisions WHERE article_id=$1 AND revision=1`, articleID).Scan(&revisionTitle, &revisionBody, &revisionHash); err != nil {
		t.Fatal(err)
	}
	if revisionTitle != savedTitle || revisionBody != savedBody || revisionHash != materials.ContentHash(savedTitle, savedBody, "") {
		t.Fatalf("current revision was not updated with the repair: title=%q body=%q hash=%q", revisionTitle, revisionBody, revisionHash)
	}
	var selected bool
	var publicTitle, searchBody string
	if err := pool.QueryRow(ctx, `SELECT selected,original_title FROM publications WHERE article_id=$1`, articleID).Scan(&selected, &publicTitle); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT body FROM pool_search WHERE article_id=$1`, articleID).Scan(&searchBody); err != nil {
		t.Fatal(err)
	}
	if !selected || publicTitle != savedTitle || !strings.Contains(searchBody, "literal < and <code>") {
		t.Fatalf("repair did not refresh the public projection and search cache: selected=%t title=%q search=%q", selected, publicTitle, searchBody)
	}
	retried, found, err := service.NormalizeXEncoding(ctx, articleID, input, actor)
	if err != nil || !found || retried == nil || retried.Status != "repaired" {
		t.Fatalf("same request did not return its original result: result=%#v found=%t err=%v", retried, found, err)
	}
	anotherRequest := input
	anotherRequest.RequestID = requestID + "-again"
	duplicate, found, err := service.NormalizeXEncoding(ctx, articleID, anotherRequest, "other-operator")
	if err != nil || !found || duplicate == nil || duplicate.Status != "already-normalized" {
		t.Fatalf("second repair request was not refused as already normalized: result=%#v found=%t err=%v", duplicate, found, err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE subject=$1 AND action=$2`, "content:"+articleID, xEncodingRepairAction).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("repair audit count=%d err=%v", auditCount, err)
	}
}

/** TestCompletedRerunRequestsDoNotResetNewerWork preserves newer processing state and manual grouping on retry. */
func TestCompletedRerunRequestsDoNotResetNewerWork(t *testing.T) {
	pool := reportCorrectionPool(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_rerun_%d", time.Now().UnixNano())
	sourceID := "source_" + fixture
	articleIDs := []string{"article_extract_" + fixture, "article_analyze_" + fixture, "article_group_" + fixture}
	actor := "admin_" + fixture
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'articleId'=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM audit_log WHERE actor=$1`, actor)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM pool_search WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM grouping_overrides WHERE article_id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=ANY($1)`, articleIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode) VALUES($1,'Rerun fixture','external','T1','editorial')`, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, articleID := range articleIDs {
		articleURL := "https://example.invalid/" + articleID
		if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,body_status,body_text,processing_state,discovered_at,timeline_at,grouping_status)
			VALUES($1,$2,$1,$3,'Rerun fixture','ok','body','failed',now(),now(),'complete')`, articleID, sourceID, articleURL); err != nil {
			t.Fatal(err)
		}
	}
	publisher := publication.NewRepository(pool, nil, interests.PublicationSettings{}, "Fixture", "https://example.invalid")
	publisher.SetEffects(testXEncodingEffects{})
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pipelineORM, err := gorm.Open(postgres.Open(os.Getenv("MYHOT_TEST_DATABASE_URL")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pipelineSQL, err := pipelineORM.DB()
	if err != nil {
		t.Fatal(err)
	}
	pipelineSQL.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = pipelineSQL.Close() })
	commands := NewCommandsRepository(pool, publisher, client, false, ReportCorrectionConfig{})
	steps := []struct {
		step      string
		articleID string
	}{
		{step: "extract", articleID: articleIDs[0]},
		{step: "analyze", articleID: articleIDs[1]},
		{step: "group", articleID: articleIDs[2]},
	}
	for _, item := range steps {
		requestID := "rerun_" + fixture + "_" + item.step
		first, found, err := commands.Rerun(ctx, item.articleID, item.step, requestID, actor)
		if err != nil || !found || first == nil || first.JobID == nil {
			t.Fatalf("first %s rerun result=%#v found=%t err=%v", item.step, first, found, err)
		}
		jobID, err := strconv.ParseInt(*first.JobID, 10, 64)
		if err != nil {
			t.Fatalf("parse %s rerun job id %q: %v", item.step, *first.JobID, err)
		}
		if item.step == "analyze" {
			var attemptTag sql.NullString
			var queuedAt sql.NullTime
			if err := pool.QueryRow(ctx, `SELECT processing_attempt_tag,processing_queued_at FROM articles WHERE id=$1`, item.articleID).Scan(&attemptTag, &queuedAt); err != nil {
				t.Fatal(err)
			}
			wantAttemptTag := "admin:" + requestID
			if !attemptTag.Valid || attemptTag.String != wantAttemptTag || !queuedAt.Valid {
				t.Fatalf("manual analysis identity was not persisted: tag=%+v queuedAt=%+v", attemptTag, queuedAt)
			}

			// Simulate a lost River row, then verify the periodic scan reconstructs its paid identity.
			if _, err := pool.Exec(ctx, `DELETE FROM river_job WHERE id=$1`, jobID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE articles SET created_at=now()-interval '10 minutes',discovered_at=now() WHERE id=$1`, item.articleID); err != nil {
				t.Fatal(err)
			}
			pipelineService := contentpipeline.NewService(contentpipeline.NewRepository(pipelineORM, true), nil, nil, nil, nil)
			analysisTasks, _, err := pipelineService.Scan(ctx, 500)
			if err != nil {
				t.Fatalf("scan lost analysis job: %v", err)
			}
			var recoveredTask *contentpipeline.AnalysisTask
			for index := range analysisTasks {
				if analysisTasks[index].ArticleID == item.articleID {
					recoveredTask = &analysisTasks[index]
					break
				}
			}
			if recoveredTask == nil || recoveredTask.AttemptTag != wantAttemptTag {
				t.Fatalf("recovered analysis task = %#v, want attempt tag %q", recoveredTask, wantAttemptTag)
			}
			inserted, err := client.Insert(ctx, jobargs.ContentAnalyzeArgs{ArticleID: recoveredTask.ArticleID, AttemptTag: recoveredTask.AttemptTag}, &river.InsertOpts{
				Queue: "shared_analysis", Priority: recoveredTask.Priority, MaxAttempts: 5,
				UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
					rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
					rivertype.JobStateRetryable, rivertype.JobStateScheduled,
				}},
			})
			if err != nil || inserted.Job == nil {
				t.Fatalf("reinsert recovered analysis task: job=%#v err=%v", inserted, err)
			}
			jobID = inserted.Job.ID
			var recoveredArgs []byte
			if err := pool.QueryRow(ctx, `SELECT args FROM river_job WHERE id=$1`, jobID).Scan(&recoveredArgs); err != nil {
				t.Fatal(err)
			}
			var recovered struct {
				AttemptTag string `json:"attemptTag"`
			}
			if err := json.Unmarshal(recoveredArgs, &recovered); err != nil || recovered.AttemptTag != wantAttemptTag {
				t.Fatalf("recovered job args=%s tag=%q err=%v", recoveredArgs, recovered.AttemptTag, err)
			}
		}
		if _, err := pool.Exec(ctx, `UPDATE river_job SET state='completed',finalized_at=now() WHERE id=$1`, jobID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE articles SET processing_state='analyzed',revision=revision+1,body_status='ok' WHERE id=$1`, item.articleID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO grouping_overrides(article_id,reason,actor) VALUES($1,'later manual detach',$2)`, item.articleID, actor); err != nil {
			t.Fatal(err)
		}
		again, found, err := commands.Rerun(ctx, item.articleID, item.step, requestID, actor)
		if err != nil || !found || again == nil || again.JobID == nil || *again.JobID != *first.JobID {
			t.Fatalf("completed %s request changed its job: first=%#v again=%#v found=%t err=%v", item.step, first, again, found, err)
		}
		var state, bodyStatus string
		var revision, overrides int
		var reason string
		if err := pool.QueryRow(ctx, `SELECT processing_state,body_status,revision FROM articles WHERE id=$1`, item.articleID).Scan(&state, &bodyStatus, &revision); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*),coalesce(max(reason),'') FROM grouping_overrides WHERE article_id=$1`, item.articleID).Scan(&overrides, &reason); err != nil {
			t.Fatal(err)
		}
		if state != "analyzed" || bodyStatus != "ok" || revision != 2 || overrides != 1 || reason != "later manual detach" {
			t.Fatalf("duplicate %s rerun reset newer state: state=%q body=%q revision=%d overrides=%d reason=%q", item.step, state, bodyStatus, revision, overrides, reason)
		}
	}
}

/** TestCorrectPublicationDateCommitsAuditedCorrectionIdempotently verifies transaction and projection invariants. */
func TestCorrectPublicationDateCommitsAuditedCorrectionIdempotently(t *testing.T) {
	pool := reportCorrectionPool(t)
	ctx := context.Background()
	fixture := fmt.Sprintf("admincontent_publication_date_%d", time.Now().UnixNano())
	sourceID, articleID := "source_"+fixture, "article_"+fixture
	actor, requestID := "operator_"+fixture, "date_"+fixture
	functionName, triggerName := "refuse_"+fixture, "refuse_"+fixture
	projectionFunction, projectionTrigger := "mutate_"+fixture, "mutate_"+fixture
	now := time.Now().UTC().Truncate(time.Millisecond)
	oldPublished := now.Add(-20 * 24 * time.Hour)
	oldClaim := oldPublished.Add(-time.Hour)
	discovered := now.Add(-19 * 24 * time.Hour)
	requested := oldPublished.Add(10 * 24 * time.Hour).Format(publicationDateLayout)
	articleURL := "https://example.invalid/" + fixture
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON audit_log", triggerName))
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON publications", projectionTrigger))
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", projectionFunction))
		_, _ = pool.Exec(ctx, `DELETE FROM audit_log WHERE subject=$1`, "content:"+articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM pool_search WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM analyses WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,participation_mode,site_fulltext) VALUES($1,'Publication date fixture','rss','editorial',true)`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,language,published_at,published_at_claim,body_text,body_status,grouping_status,selection_adds_value,discovered_at,timeline_at)
		VALUES($1,$2,$3,$4,'Historical fixture','en',$5,$6,'Confirmed body','ok','complete',true,$7,$5)`,
		articleID, sourceID, "url:"+articleURL, articleURL, oldPublished, oldClaim, discovered); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analyses(article_id,input_revision,origin,relevance,category,tags,subjects,title_zh,summary_zh,reason_zh,score,selected,output)
		VALUES($1,1,'model','pass','ai-model',ARRAY['model'],ARRAY['example'],'Historical fixture','Summary','Reason',8.5,true,'{"scope":"single"}'::jsonb)`, articleID); err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	settings := interests.PublicationSettings{PublicCategories: interests.PublicCategories{Merge: map[string]string{"ai-model": "technology"}}}
	publisher := publication.NewRepository(pool, nil, settings, "Fixture", "https://example.invalid")
	publisher.SetEffects(testXEncodingEffects{})
	if published, err := publisher.Publish(ctx, articleID, publication.PublishOptions{}); err != nil || published == nil || !published.Selected {
		t.Fatalf("fixture publication=%#v err=%v", published, err)
	}
	commands := NewCommandsRepository(pool, publisher, client, false, ReportCorrectionConfig{})
	service := NewService(nil)
	service.SetPublicationDateCorrectionRepository(commands)
	plan, err := service.PreviewPublicationDateCorrection(ctx, articleID, requested)
	if err != nil || plan == nil || !plan.Changed || !plan.TimelineFollowsPublication {
		t.Fatalf("preview plan=%#v err=%v", plan, err)
	}
	stale := PublicationDateCorrectionInput{Version: plan.Version + 1, Hash: plan.Hash, PublishedAt: requested, RequestID: requestID, Reason: "verified source timestamp"}
	if _, found, err := service.CorrectPublicationDate(ctx, articleID, stale, actor); !found || !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("stale approval found=%t err=%v", found, err)
	}
	input := PublicationDateCorrectionInput{Version: plan.Version, Hash: plan.Hash, PublishedAt: requested, RequestID: requestID, Reason: "verified source timestamp"}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.article_id='%s' AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN NEW.summary='unexpected decision change'; END IF; RETURN NEW; END; $$`, projectionFunction, articleID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON publications FOR EACH ROW EXECUTE FUNCTION %s()`, projectionTrigger, projectionFunction)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.CorrectPublicationDate(ctx, articleID, input, actor); !found || !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("changed public decision was not rejected: found=%t err=%v", found, err)
	}
	var decisionRollbackPublished time.Time
	var decisionRollbackSummary string
	if err := pool.QueryRow(ctx, `SELECT a.published_at,p.summary FROM articles a JOIN publications p ON p.article_id=a.id WHERE a.id=$1`, articleID).
		Scan(&decisionRollbackPublished, &decisionRollbackSummary); err != nil {
		t.Fatal(err)
	}
	if !decisionRollbackPublished.Equal(oldPublished) || decisionRollbackSummary != "Summary" {
		t.Fatalf("decision-change conflict did not roll back: published=%s summary=%q", decisionRollbackPublished, decisionRollbackSummary)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER %s ON publications", projectionTrigger)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP FUNCTION %s()", projectionFunction)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END; $$`, functionName, publicationDateCorrectionAction)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CorrectPublicationDate(ctx, articleID, input, actor); err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("audit failure did not stop correction: %v", err)
	}
	var rolledBackPublished, rolledBackTimeline time.Time
	if err := pool.QueryRow(ctx, `SELECT published_at,timeline_at FROM articles WHERE id=$1`, articleID).Scan(&rolledBackPublished, &rolledBackTimeline); err != nil {
		t.Fatal(err)
	}
	if !rolledBackPublished.Equal(oldPublished) || !rolledBackTimeline.Equal(oldPublished) {
		t.Fatalf("date correction survived its audit failure: published=%s timeline=%s", rolledBackPublished, rolledBackTimeline)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER %s ON audit_log", triggerName)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("DROP FUNCTION %s()", functionName)); err != nil {
		t.Fatal(err)
	}
	result, found, err := service.CorrectPublicationDate(ctx, articleID, input, actor)
	if err != nil || !found || result == nil || result.Status != "corrected" || result.PublishedAt != requested {
		t.Fatalf("correction result=%#v found=%t err=%v", result, found, err)
	}
	var savedPublished, savedClaim, savedTimeline, savedDiscovered time.Time
	var revision int
	var body string
	if err := pool.QueryRow(ctx, `SELECT published_at,published_at_claim,timeline_at,discovered_at,revision,body_text FROM articles WHERE id=$1`, articleID).
		Scan(&savedPublished, &savedClaim, &savedTimeline, &savedDiscovered, &revision, &body); err != nil {
		t.Fatal(err)
	}
	if !savedPublished.Equal(oldPublished.Add(10*24*time.Hour)) || !savedClaim.Equal(savedPublished) || !savedTimeline.Equal(savedPublished) ||
		!savedDiscovered.Equal(discovered) || revision != 1 || body != "Confirmed body" {
		t.Fatalf("correction changed unrelated material or lost date fields: published=%s claim=%s timeline=%s discovered=%s revision=%d body=%q",
			savedPublished, savedClaim, savedTimeline, savedDiscovered, revision, body)
	}
	retried, found, err := service.CorrectPublicationDate(ctx, articleID, input, actor)
	if err != nil || !found || retried == nil || retried.Status != "corrected" || retried.PublishedAt != requested {
		t.Fatalf("same request was not idempotent: result=%#v found=%t err=%v", retried, found, err)
	}
	conflicting := input
	conflicting.PublishedAt = oldPublished.Add(9 * 24 * time.Hour).Format(publicationDateLayout)
	if _, found, err := service.CorrectPublicationDate(ctx, articleID, conflicting, actor); !found || !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("request id reuse approved a different date: found=%t err=%v", found, err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE subject=$1 AND action=$2`, "content:"+articleID, publicationDateCorrectionAction).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("correction audit count=%d err=%v", auditCount, err)
	}
}
