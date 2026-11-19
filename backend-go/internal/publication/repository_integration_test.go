package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testEffects struct {
	selected, media, changed int
	fail                     bool
}

func (e *testEffects) EnqueueSelectedTx(context.Context, pgx.Tx, string, time.Time) error {
	e.selected++
	if e.fail {
		return errors.New("fixture queue failure")
	}
	return nil
}
func (e *testEffects) EnqueueMediaTx(context.Context, pgx.Tx, string) error {
	e.media++
	if e.fail {
		return errors.New("fixture queue failure")
	}
	return nil
}
func (e *testEffects) ArticleChangedTx(context.Context, pgx.Tx, ArticleChanged) error {
	e.changed++
	if e.fail {
		return errors.New("fixture event failure")
	}
	return nil
}

func TestRepositoryPublishIsIdempotentAcrossProjectionSearchAndSelectedLedger(t *testing.T) {
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	prefix := fmt.Sprintf("publication_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	gatedArticleID := prefix + "_gated_article"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=ANY($1)`, []string{articleID, gatedArticleID})
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=ANY($1)`, []string{articleID, gatedArticleID})
		_, _ = pool.Exec(ctx, `DELETE FROM pool_search WHERE article_id=ANY($1)`, []string{articleID, gatedArticleID})
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=ANY($1)`, []string{articleID, gatedArticleID})
		_, _ = pool.Exec(ctx, `DELETE FROM analyses WHERE article_id=ANY($1)`, []string{articleID, gatedArticleID})
		_, _ = pool.Exec(ctx, `DELETE FROM editorial_overrides WHERE article_id=ANY($1)`, []string{articleID, gatedArticleID})
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=ANY($1)`, []string{articleID, gatedArticleID})
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,participation_mode,site_fulltext) VALUES($1,'OpenAI（官网）','rss','editorial',true)`, sourceID); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,language,published_at,discovered_at,timeline_at,body_status,body_text,grouping_status,selection_adds_value)
		VALUES($1,$2,$1,'https://publisher.example/story','OpenAI 发布新模型','zh',$3,$3,$3,'ok','A full body','complete',true)`, articleID, sourceID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analyses(article_id,input_revision,origin,relevance,category,tags,subjects,title_zh,summary_zh,reason_zh,score,selected,output)
		VALUES($1,1,'model','pass','ai-model',ARRAY['大模型'],ARRAY['openai'],'新模型发布','模型摘要','发布原因',8.5,true,$2::jsonb)`, articleID, `{"scope":"single"}`); err != nil {
		t.Fatal(err)
	}
	settings := interests.PublicationSettings{PublicCategories: interests.PublicCategories{Merge: map[string]string{"ai-model": "technology"}}}
	repository := NewRepository(pool, nil, settings, "Example", "https://example.test")
	failedEffects := &testEffects{fail: true}
	repository.SetEffects(failedEffects)
	if _, err := repository.Publish(ctx, articleID, PublishOptions{Now: at}); err == nil {
		t.Fatal("failed queue insert should roll back publication")
	}
	var afterFailure int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM publications WHERE article_id=$1`, articleID).Scan(&afterFailure); err != nil {
		t.Fatal(err)
	}
	if afterFailure != 0 {
		t.Fatalf("publication survived failed River insert: %d", afterFailure)
	}
	effects := &testEffects{}
	repository.SetEffects(effects)
	first, err := repository.Publish(ctx, articleID, PublishOptions{Now: at})
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || !first.Changed || !first.Selected || first.Ledger == nil || *first.Ledger != "upsert" {
		t.Fatalf("first publish = %#v", first)
	}
	second, err := repository.Publish(ctx, articleID, PublishOptions{Now: at.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.Changed || second.Ledger != nil {
		t.Fatalf("repeat publish = %#v", second)
	}
	var revision, ledgerCount int
	var direct, body string
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT revision FROM publications WHERE article_id=$1`, articleID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selected_ledger WHERE article_id=$1`, articleID).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT direct,body FROM pool_search WHERE article_id=$1`, articleID).Scan(&direct, &body); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT payload FROM selected_ledger WHERE article_id=$1 AND op='upsert'`, articleID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal(payload, &item); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || ledgerCount != 1 || !strings.Contains(direct, "openai") || body != "a full body" || item["category"] != "technology" {
		t.Fatalf("stored projection effects revision=%d ledger=%d direct=%q body=%q payload=%s", revision, ledgerCount, direct, body, payload)
	}
	if effects.selected != 1 || effects.media != 1 || effects.changed != 0 {
		t.Fatalf("unexpected transactional effects: %#v", effects)
	}

	staleGroupedAt := at.Add(-time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,language,published_at,discovered_at,timeline_at,body_status,body_text,grouping_status,grouped_at,selection_adds_value)
		VALUES($1,$2,$1,'https://publisher.example/gated','OpenAI 发布跟进','zh',$3,$3,$3,'ok','A follow-up body','failed',$4,true)`, gatedArticleID, sourceID, at, staleGroupedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analyses(article_id,input_revision,origin,relevance,category,tags,subjects,title_zh,summary_zh,reason_zh,score,selected,output)
		VALUES($1,1,'model','pass','ai-model',ARRAY['大模型'],ARRAY['openai'],'新模型跟进','跟进摘要','跟进原因',8.5,true,$2::jsonb)`, gatedArticleID, `{"scope":"single"}`); err != nil {
		t.Fatal(err)
	}
	failedGrouping, err := repository.Publish(ctx, gatedArticleID, PublishOptions{Now: at})
	if err != nil || failedGrouping == nil || failedGrouping.Selected {
		t.Fatalf("stale failed grouping was admitted: result=%#v err=%v", failedGrouping, err)
	}
	var selectionCandidate, selected bool
	var visibleAfter *time.Time
	if err := pool.QueryRow(ctx, `SELECT selection_candidate,selected,visible_after FROM publications WHERE article_id=$1`, gatedArticleID).Scan(&selectionCandidate, &selected, &visibleAfter); err != nil {
		t.Fatal(err)
	}
	var gatedLedgerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selected_ledger WHERE article_id=$1`, gatedArticleID).Scan(&gatedLedgerCount); err != nil {
		t.Fatal(err)
	}
	if !selectionCandidate || selected || visibleAfter != nil || gatedLedgerCount != 0 || effects.selected != 1 {
		t.Fatalf("failed grouping published too early: candidate=%v selected=%v visible_after=%v ledger=%d effects=%#v", selectionCandidate, selected, visibleAfter, gatedLedgerCount, effects)
	}

	if _, err := pool.Exec(ctx, `UPDATE articles SET grouping_status='complete',grouped_at=now(),selection_adds_value=true WHERE id=$1`, gatedArticleID); err != nil {
		t.Fatal(err)
	}
	completedGrouping, err := repository.Publish(ctx, gatedArticleID, PublishOptions{Now: at.Add(time.Minute)})
	if err != nil || completedGrouping == nil || !completedGrouping.Selected {
		t.Fatalf("completed grouping was not selected: result=%#v err=%v", completedGrouping, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selected_ledger WHERE article_id=$1 AND op='upsert'`, gatedArticleID).Scan(&gatedLedgerCount); err != nil || gatedLedgerCount != 1 {
		t.Fatalf("completed grouping ledger upsert count=%d err=%v", gatedLedgerCount, err)
	}
	if effects.selected != 2 || effects.media != 2 {
		t.Fatalf("completed selection did not enqueue one notification and media task: %#v", effects)
	}

	if _, err := pool.Exec(ctx, `UPDATE articles SET selection_adds_value=false WHERE id=$1`, gatedArticleID); err != nil {
		t.Fatal(err)
	}
	redundantGrouping, err := repository.Publish(ctx, gatedArticleID, PublishOptions{Now: at.Add(2 * time.Minute)})
	if err != nil || redundantGrouping == nil || redundantGrouping.Selected {
		t.Fatalf("redundant explanation stayed selected: result=%#v err=%v", redundantGrouping, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO editorial_overrides(article_id,fields,reason,updated_by)
		VALUES($1,'{"selected":true}','editor verified independent value','test')`, gatedArticleID); err != nil {
		t.Fatal(err)
	}
	manualSelection, err := repository.Publish(ctx, gatedArticleID, PublishOptions{Now: at.Add(3 * time.Minute)})
	if err != nil || manualSelection == nil || !manualSelection.Selected {
		t.Fatalf("explicit editorial selection was not applied: result=%#v err=%v", manualSelection, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM selected_ledger WHERE article_id=$1 AND op='upsert'`, gatedArticleID).Scan(&gatedLedgerCount); err != nil || gatedLedgerCount != 2 {
		t.Fatalf("manual selection ledger upsert count=%d err=%v", gatedLedgerCount, err)
	}
}

/** TestRepositorySettlesOneSelectedSeatPerFactKeepsTheLedgerCurrent checks representative changes. */
func TestRepositorySettlesOneSelectedSeatPerFactKeepsTheLedgerCurrent(t *testing.T) {
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	prefix := fmt.Sprintf("publication_seat_%d", time.Now().UnixNano())
	mediaSource, officialSource := prefix+"_media_source", prefix+"_official_source"
	mediaArticle, officialArticle := prefix+"_media_article", prefix+"_official_article"
	var factID int64
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=ANY($1)`, []string{mediaArticle, officialArticle})
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=ANY($1)`, []string{mediaArticle, officialArticle})
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=ANY($1)`, []string{mediaArticle, officialArticle})
		_, _ = pool.Exec(ctx, `DELETE FROM fact_articles WHERE fact_id=$1`, factID)
		_, _ = pool.Exec(ctx, `DELETE FROM facts WHERE id=$1`, factID)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=ANY($1)`, []string{mediaArticle, officialArticle})
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=ANY($1)`, []string{mediaSource, officialSource})
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,first_party,participation_mode)
		VALUES($1,'Media fixture','rss','T2',false,'editorial'),($2,'Official fixture','rss','T1',true,'editorial')`, mediaSource, officialSource); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO facts(public_id,title,subject) VALUES($1,'Fixture launch','OpenAI') RETURNING id`, prefix+"_fact").Scan(&factID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, article := range []struct{ id, source string }{{mediaArticle, mediaSource}, {officialArticle, officialSource}} {
		if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at)
			VALUES($1,$2,$1,$3,$4,$5,$5)`, article.id, article.source, "https://fixture.example/"+article.id, article.id, now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO fact_articles(fact_id,article_id,role) VALUES($1,$2,'report')`, factID, article.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO publications(article_id,source_id,title,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,selected,seat,body_mode,score,fact_id)
		VALUES($1,$2,'Media report','https://fixture.example/media','news',$3,$3,$3,'public',true,true,true,'summary',80,$4),
		      ($5,$6,'Official report','https://fixture.example/official','news',$3,$3,$3,'public',true,true,false,'summary',60,$4)`, mediaArticle, mediaSource, now, factID, officialArticle, officialSource); err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(pool, nil, interests.PublicationSettings{}, "Example", "https://example.test")
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.syncLedger(ctx, seed, mediaArticle, now); err != nil {
		_ = seed.Rollback(ctx)
		t.Fatal(err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := repository.settleSeats(ctx, tx, factID, officialArticle, now.Add(time.Second))
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if operation == nil || *operation != "upsert" {
		t.Fatalf("new representative ledger operation = %v, want upsert", operation)
	}
	var mediaSeat, officialSeat, mediaInSet, officialInSet bool
	if err := pool.QueryRow(ctx, `SELECT seat FROM publications WHERE article_id=$1`, mediaArticle).Scan(&mediaSeat); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT seat FROM publications WHERE article_id=$1`, officialArticle).Scan(&officialSeat); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT in_set FROM selected_state WHERE article_id=$1`, mediaArticle).Scan(&mediaInSet); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT in_set FROM selected_state WHERE article_id=$1`, officialArticle).Scan(&officialInSet); err != nil {
		t.Fatal(err)
	}
	if mediaSeat || !officialSeat || mediaInSet || !officialInSet {
		t.Fatalf("selected representative state media=(seat %v,in_set %v) official=(seat %v,in_set %v)", mediaSeat, mediaInSet, officialSeat, officialInSet)
	}
	for article, want := range map[string]string{mediaArticle: "remove", officialArticle: "upsert"} {
		var latest string
		if err := pool.QueryRow(ctx, `SELECT op FROM selected_ledger WHERE article_id=$1 ORDER BY seq DESC LIMIT 1`, article).Scan(&latest); err != nil || latest != want {
			t.Fatalf("latest ledger operation for %s = %q, err=%v; want %q", article, latest, err, want)
		}
	}
}
