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
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM selected_state WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM selected_ledger WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM pool_search WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM analyses WHERE article_id=$1`, articleID)
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE id=$1`, articleID)
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
	if revision != 1 || ledgerCount != 1 || !strings.Contains(direct, "OpenAI") || body != "a full body" || item["category"] != "technology" {
		t.Fatalf("stored projection effects revision=%d ledger=%d direct=%q body=%q payload=%s", revision, ledgerCount, direct, body, payload)
	}
	if effects.selected != 1 || effects.media != 1 || effects.changed != 0 {
		t.Fatalf("unexpected transactional effects: %#v", effects)
	}
}
