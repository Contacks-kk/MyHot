package admincontent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
	dailyKey, weeklyKey := fixture+"-daily", fixture+"-weekly"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM report_revisions WHERE report_id IN (SELECT id FROM reports WHERE key=ANY($1))`, []string{dailyKey, weeklyKey})
		_, _ = pool.Exec(ctx, `DELETE FROM reports WHERE key=ANY($1)`, []string{dailyKey, weeklyKey})
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
	daily, _ := json.Marshal(map[string]any{"sections": []any{map[string]any{"label": "Industry", "items": []any{entry}}}, "metrics": map[string]any{"modelsReleased": 0}})
	weekly, _ := json.Marshal(map[string]any{"themes": []any{map[string]any{"heading": "Industry", "summary": "old", "storyRefs": []any{entry}}}})
	start, end := time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)
	for _, row := range []struct {
		kind, key string
		content   []byte
	}{{"daily", dailyKey, daily}, {"weekly", weeklyKey, weekly}} {
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
	var dailyContent, weeklyContent json.RawMessage
	var dailyRevision, weeklyRevision int
	if err := pool.QueryRow(ctx, `SELECT content,revision FROM reports WHERE key=$1`, dailyKey).Scan(&dailyContent, &dailyRevision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT content,revision FROM reports WHERE key=$1`, weeklyKey).Scan(&weeklyContent, &weeklyRevision); err != nil {
		t.Fatal(err)
	}
	var d, w map[string]any
	_ = json.Unmarshal(dailyContent, &d)
	_ = json.Unmarshal(weeklyContent, &w)
	dSections := reportGroups(d["sections"])
	wThemes := reportGroups(w["themes"])
	if dailyRevision != 2 || len(dSections) != 1 || dSections[0]["label"] != "Model" || reportEntries(dSections[0]["items"], articleID) != 1 || d["metrics"].(map[string]any)["modelsReleased"] != float64(1) {
		t.Fatalf("daily frozen entry was not moved/recounted: revision=%d content=%s", dailyRevision, dailyContent)
	}
	if weeklyRevision != 2 || len(wThemes) != 1 || wThemes[0]["heading"] != "Model" || reportEntries(wThemes[0]["storyRefs"], articleID) != 1 || wThemes[0]["summary"] != nil {
		t.Fatalf("weekly frozen reference was not moved/reset: revision=%d content=%s", weeklyRevision, weeklyContent)
	}
	var archived int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_revisions rr JOIN reports r ON r.id=rr.report_id WHERE r.key=ANY($1)`, []string{dailyKey, weeklyKey}).Scan(&archived); err != nil || archived != 2 {
		t.Fatalf("old report contents were not archived: revisions=%d err=%v", archived, err)
	}
}
