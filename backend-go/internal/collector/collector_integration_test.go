package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/materials"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type fakeDoer func(*http.Request) (*http.Response, error)

func (call fakeDoer) Do(r *http.Request) (*http.Response, error) { return call(r) }

func collectorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("database must be localhost and end with _test")
	}
	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func collectorORM(t *testing.T) *gorm.DB {
	t.Helper()
	orm, err := gorm.Open(postgres.Open(os.Getenv("MYHOT_TEST_DATABASE_URL")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return orm
}

func testCollectorService(orm *gorm.DB, pool *pgxpool.Pool, client HTTPDoer) *Service {
	return NewService(NewRepository(orm, pool, config.SourceSchedule{}), client, materials.NewService(materials.NewRepository(orm)))
}

func addCollectorSubscription(t *testing.T, pool *pgxpool.Pool, sourceIDs ...string) int64 {
	t.Helper()
	ctx := context.Background()
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash) VALUES($1,'fixture') RETURNING id`, fmt.Sprintf("collector_user_%d@example.invalid", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM user_accounts WHERE id=$1`, userID) })
	for _, sourceID := range sourceIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO user_source_subscriptions(user_id,source_id,name) VALUES($1,$2,'Collector test')`, userID, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	return userID
}

/** TestSuccessfulCollectionStartsNaming verifies that only real fetch success supplies an actor. */
func TestSuccessfulCollectionStartsNaming(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		manual    bool
		wantReady bool
	}{
		{name: "manual 304", status: http.StatusNotModified, manual: true, wantReady: true},
		{name: "scheduled success", status: http.StatusOK, wantReady: true},
		{name: "fetch failure", status: http.StatusBadGateway, manual: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := collectorPool(t)
			orm := collectorORM(t)
			ctx := context.Background()
			id := fmt.Sprintf("collector_naming_%d", time.Now().UnixNano())
			const address = "https://example.invalid/feed"
			if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,cursor,next_fetch_at) VALUES
				($1,'个人RSS','rss',jsonb_build_object('feedUrl',$2::text),jsonb_build_object('rssURL',$2::text,'rssETag','v1','rssTitle','Saved channel'),now())`, id, address); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, id) })
			userID := addCollectorSubscription(t, pool, id)
			if _, err := pool.Exec(ctx, `UPDATE user_accounts SET verified_at=now() WHERE id=$1`, userID); err != nil {
				t.Fatal(err)
			}
			service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
				if tc.status == http.StatusNotModified && req.Header.Get("If-None-Match") != "v1" {
					t.Fatalf("conditional request omitted the stored ETag")
				}
				body := `<rss version="2.0"><channel><title>Current channel</title><link>https://example.invalid</link><description>Feed</description></channel></rss>`
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			var actor int64
			if tc.manual {
				actor = userID
			}
			collected, err := service.CollectSourceJob(ctx, id, tc.manual, actor)
			if collected != tc.wantReady || (err == nil) != tc.wantReady {
				t.Fatalf("collected=%t, err=%v", collected, err)
			}
			var status, title string
			var gotActor int64
			if err := pool.QueryRow(ctx, `SELECT coalesce(n.status,''),coalesce(n.user_id,0),coalesce(s.cursor->>'rssTitle','')
				FROM sources s LEFT JOIN source_naming n ON n.source_id=s.id WHERE s.id=$1`, id).Scan(&status, &gotActor, &title); err != nil {
				t.Fatal(err)
			}
			if tc.wantReady && (status != "ready" || gotActor != actor) || !tc.wantReady && status != "" {
				t.Fatalf("naming status=%q, actor=%d", status, gotActor)
			}
			if tc.status == http.StatusNotModified && title != "Saved channel" {
				t.Fatalf("304 lost naming evidence: %q", title)
			}
		})
	}
}

func TestDueSourceIDsRequiresActiveSubscriptionAndOnlyCollectsRSS(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	tx := orm.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if err := tx.Exec("UPDATE sources SET enabled = false").Error; err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{
		"rss":       fmt.Sprintf("collector_due_rss_%d", time.Now().UnixNano()),
		"web_list":  fmt.Sprintf("collector_due_web_%d", time.Now().UnixNano()),
		"json_list": fmt.Sprintf("collector_due_json_%d", time.Now().UnixNano()),
		"x_search":  fmt.Sprintf("collector_due_x_%d", time.Now().UnixNano()),
	}
	var userID int64
	if err := tx.Raw(`INSERT INTO user_accounts(email,password_hash) VALUES(?, 'fixture') RETURNING id`, fmt.Sprintf("collector_due_user_%d@example.invalid", time.Now().UnixNano())).Row().Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for kind, id := range ids {
		if err := tx.Exec(`INSERT INTO sources(id,name,kind,config,enabled,next_fetch_at) VALUES(?,?,?,'{}',true,now()-interval '1 minute')`, id, "Due scanner test", kind).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec(`INSERT INTO user_source_subscriptions(user_id,source_id,name) VALUES(?,?,?)`, userID, id, kind).Error; err != nil {
			t.Fatal(err)
		}
	}
	due, err := NewRepository(tx, pool, config.SourceSchedule{}).DueSourceIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(due))
	for _, id := range due {
		got[id] = true
	}
	if !got[ids["rss"]] {
		t.Fatal("RSS due-source scan omitted an actively subscribed source")
	}
	for _, kind := range []string{"web_list", "json_list", "x_search"} {
		if got[ids[kind]] {
			t.Errorf("RSS due-source scan selected unsupported user source kind %s", kind)
		}
	}
}

func TestCollectorStoresDeduplicatesAndKeepsHistoricalBody(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_%d", time.Now().UnixNano())
	address := "https://source.example.invalid/" + sourceID
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Collector test','rss',jsonb_build_object('feedUrl',$2::text))`, sourceID, address+"/feed")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)
	body := "original body"
	calls := 0
	service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != address+"/feed" {
			t.Fatalf("unexpected source URL %s", req.URL)
		}
		data := fmt.Sprintf(`<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Feed</title><link>%s</link><description>Feed</description><item><title>OpenAI news</title><link>%s/article</link><description>Summary</description><content:encoded>%s</content:encoded></item><item><title>Duplicate</title><link>%s/article?utm_source=another</link></item></channel></rss>`, address, address, body, address)
		return &http.Response{StatusCode: 200, Header: http.Header{"Etag": {`"collector-v1"`}, "Last-Modified": {"Wed, 01 Oct 2025 00:00:00 GMT"}}, Body: io.NopCloser(strings.NewReader(data))}, nil
	}))
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var etag, lastModified, cursorURL string
	if err := pool.QueryRow(ctx, `SELECT cursor->>'rssETag',cursor->>'rssLastModified',cursor->>'rssURL' FROM sources WHERE id=$1`, sourceID).Scan(&etag, &lastModified, &cursorURL); err != nil {
		t.Fatal(err)
	}
	if etag != `"collector-v1"` || lastModified != "Wed, 01 Oct 2025 00:00:00 GMT" || cursorURL != address+"/feed" {
		t.Fatalf("RSS validators were not persisted: %q / %q / %q", etag, lastModified, cursorURL)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1`, sourceID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("article count = %d: %v", count, err)
	}
	otherSourceID := sourceID + "_mirror"
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Mirror test','rss','{}')`, otherSourceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, otherSourceID) })
	mirrorResult, err := testCollectorService(orm, pool, nil).Materials.Store(ctx, materials.Input{
		SourceID: otherSourceID, URL: address + "/article", IdentityKey: "url:" + address + "/article",
		Title: "Mirror rendering", BodyText: "different mirror body", Via: "ingest",
	})
	if err != nil || mirrorResult.Created || mirrorResult.Revised || mirrorResult.ArticleID == "" {
		t.Fatalf("cross-source discovery result = %#v: %v", mirrorResult, err)
	}
	var discoveries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM article_discoveries WHERE article_id=$1`, mirrorResult.ArticleID).Scan(&discoveries); err != nil || discoveries != 2 {
		t.Fatalf("article discovery count = %d: %v", discoveries, err)
	}
	var keptTitle, keptBody string
	if err := pool.QueryRow(ctx, `SELECT title,body_text FROM articles WHERE id=$1`, mirrorResult.ArticleID).Scan(&keptTitle, &keptBody); err != nil || keptTitle != "OpenAI news" || keptBody != body {
		t.Fatalf("cross-source material overwrote original content %q/%q: %v", keptTitle, keptBody, err)
	}
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("source interval was ignored")
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_fetch_at=now()-interval '1 second' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var revision int
	var articleID string
	if err := pool.QueryRow(ctx, `SELECT id,revision FROM articles WHERE source_id=$1`, sourceID).Scan(&articleID, &revision); err != nil || revision != 1 {
		t.Fatalf("unchanged article revision = %d: %v", revision, err)
	}
	var backfill bool
	var backfillReason string
	if err := pool.QueryRow(ctx, `SELECT backfill,backfill_reason FROM articles WHERE id=$1`, articleID).Scan(&backfill, &backfillReason); err != nil || !backfill || backfillReason != "first-import" {
		t.Fatalf("first import backfill = %t/%q: %v", backfill, backfillReason, err)
	}
	body = "original bod�"
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_fetch_at=now()-interval '1 second' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var saved string
	if err := pool.QueryRow(ctx, `SELECT body_text FROM articles WHERE id=$1`, articleID).Scan(&saved); err != nil || saved != "original body" {
		t.Fatalf("replacement-character refresh changed body to %q: %v", saved, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE articles SET processing_state='analyzed', processing_attempts=4,
		grouping_status='complete', selection_adds_value=true WHERE id=$1`, articleID); err != nil {
		t.Fatal(err)
	}
	body = "a longer updated source body"
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_fetch_at=now()-interval '1 second' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var processingState, groupingStatus string
	var attempts, updatedRevision int
	var hasSelectionValue bool
	if err := pool.QueryRow(ctx, `SELECT processing_state,processing_attempts,grouping_status,selection_adds_value,revision
		FROM articles WHERE id=$1`, articleID).Scan(&processingState, &attempts, &groupingStatus, &hasSelectionValue, &updatedRevision); err != nil {
		t.Fatal(err)
	}
	if processingState != "new" || attempts != 0 || groupingStatus != "pending" || hasSelectionValue || updatedRevision != 2 {
		t.Fatalf("revision did not reset processing: state=%s attempts=%d grouping=%s selection=%t revision=%d", processingState, attempts, groupingStatus, hasSelectionValue, updatedRevision)
	}
	// A historical archive is kept read-only even if the source starts returning richer body text.
	if _, err := pool.Exec(ctx, `UPDATE articles SET created_at=(SELECT activated_at FROM personalization_rollout WHERE id=true)-interval '1 second' WHERE id=$1`, articleID); err != nil {
		t.Fatal(err)
	}
	body = "different and much longer source body"
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_fetch_at=now()-interval '1 second' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT body_text FROM articles WHERE id=$1`, articleID).Scan(&saved); err != nil || saved != "original body" {
		t.Fatalf("historical body = %q: %v", saved, err)
	}
}

func TestCollectorFailureDoesNotExposeProviderSecret(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_failure_%d", time.Now().UnixNano())
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Failure test','rss','{"feedUrl":"https://source.example.invalid/feed?secret=private-source-key"}')`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID) })
	addCollectorSubscription(t, pool, sourceID)
	service := testCollectorService(orm, pool, fakeDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("private-provider-key"))}, nil
	}))
	err = service.CollectSource(ctx, sourceID)
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("collection error = %v", err)
	}
	var saved string
	var failures int
	if err := pool.QueryRow(ctx, `SELECT last_error,fail_count FROM sources WHERE id=$1`, sourceID).Scan(&saved, &failures); err != nil || strings.Contains(saved, "private") || failures != 1 {
		t.Fatalf("stored error %q/failures=%d: %v", saved, failures, err)
	}
}

func TestReuseWindowSkipsFetchAndOnlyAdvancesAutomaticSchedule(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_reuse_%d", time.Now().UnixNano())
	address := "https://reuse.example.invalid/" + sourceID + "/feed"
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,enabled,interval_minutes,reuse_window_minutes,last_ok_at,next_fetch_at)
		VALUES($1,'Reuse test','rss',jsonb_build_object('feedUrl',$2::text),true,60,10,now()-interval '5 minutes',now()-interval '1 minute')`, sourceID, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID) })
	addCollectorSubscription(t, pool, sourceID)
	var calls int
	service := testCollectorService(orm, pool, fakeDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("network request should be reused")
	}))
	var lastOKBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT last_ok_at FROM sources WHERE id=$1`, sourceID).Scan(&lastOKBefore); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var lastOKAfter, nextFetch time.Time
	var automaticStatus string
	if err := pool.QueryRow(ctx, `SELECT last_ok_at,next_fetch_at FROM sources WHERE id=$1`, sourceID).Scan(&lastOKAfter, &nextFetch); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM fetch_runs WHERE source_id=$1 ORDER BY id DESC LIMIT 1`, sourceID).Scan(&automaticStatus); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || automaticStatus != "skipped" || !lastOKAfter.Equal(lastOKBefore) || time.Until(nextFetch) < 50*time.Minute {
		t.Fatalf("automatic reuse calls=%d status=%q lastOK=%s/%s next=%s", calls, automaticStatus, lastOKBefore, lastOKAfter, nextFetch)
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_fetch_at=now()+interval '3 hours' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var manualNext time.Time
	var manualStatus string
	if err := pool.QueryRow(ctx, `SELECT next_fetch_at FROM sources WHERE id=$1`, sourceID).Scan(&manualNext); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM fetch_runs WHERE source_id=$1 ORDER BY id DESC LIMIT 1`, sourceID).Scan(&manualStatus); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || manualStatus != "skipped" || time.Until(manualNext) < 2*time.Hour {
		t.Fatalf("manual reuse calls=%d status=%q next=%s", calls, manualStatus, manualNext)
	}
}

func TestManualCollectionForcesPausedNotYetDueSource(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_manual_%d", time.Now().UnixNano())
	address := "https://manual.example.invalid/" + sourceID
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,enabled,next_fetch_at)
		VALUES($1,'Manual fetch test','rss',jsonb_build_object('feedUrl',$2::text),false,now()+interval '1 day')`, sourceID, address+"/feed")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)
	calls := 0
	service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != address+"/feed" {
			t.Fatalf("unexpected source URL %s", req.URL)
		}
		body := fmt.Sprintf(`<rss version="2.0"><channel><title>Feed</title><link>%s</link><description>Feed</description><item><title>Manual article</title><link>%s/article</link></item></channel></rss>`, address, address)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("scheduled collection bypassed paused and due-time gates")
	}
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("manual collection calls = %d, want 1", calls)
	}
	var runs, articles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fetch_runs WHERE source_id=$1 AND status='ok'`, sourceID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("manual fetch run count = %d: %v", runs, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1 AND title='Manual article'`, sourceID).Scan(&articles); err != nil || articles != 1 {
		t.Fatalf("manual article count = %d: %v", articles, err)
	}
}

func TestXSourceCollectionPersistsPostAndAdvancesWatermark(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_x_%d", time.Now().UnixNano())
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,enabled,next_fetch_at)
		VALUES($1,'X source test','x_search','{"query":"from:demo -filter:replies","searchType":"Latest"}',true,now()-interval '1 minute')`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)
	provider := &scriptedXProvider{search: func(call xSearchCall) (XPage, error) {
		if call.query != "from:demo -filter:replies" || call.subject != "source:"+sourceID {
			t.Fatalf("X search call = %#v", call)
		}
		return XPage{Tweets: []tweet{{ID: "987654321012345678", CreatedAt: "2026-10-01T02:03:04Z", FullText: "A post", Lang: "en", User: xUser{Name: "Demo", Handle: "demo"}}}}, nil
	}}
	service := testCollectorService(orm, pool, nil)
	service.XProvider = provider
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var cursor json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT cursor FROM sources WHERE id=$1`, sourceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	var savedCursor map[string]any
	if err := json.Unmarshal(cursor, &savedCursor); err != nil || savedCursor["lastTweetId"] != "987654321012345678" || savedCursor["initializedAt"] == nil {
		t.Fatalf("saved X cursor = %s, error=%v", cursor, err)
	}
	var title, identity string
	var xPost, media json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT title,identity_key,x_post,media FROM articles WHERE source_id=$1`, sourceID).Scan(&title, &identity, &xPost, &media); err != nil {
		t.Fatal(err)
	}
	if title != "A post" || identity != "x:987654321012345678" || len(xPost) == 0 || string(media) != "[]" {
		t.Fatalf("stored X material title=%q identity=%q x_post=%s media=%s", title, identity, xPost, media)
	}
}

func TestXShardCollectionRoutesPostsAndRecordsEachAccount(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	ids := []string{"collector_x_shard_a_" + suffix, "collector_x_shard_b_" + suffix, "collector_x_shard_c_" + suffix}
	handles := []string{"alpha_" + suffix[:8], "beta_" + suffix[:8], "gamma_" + suffix[:8]}
	for i, id := range ids {
		config := fmt.Sprintf(`{"query":"from:%s -filter:replies","searchType":"Latest"}`, handles[i])
		cursor := fmt.Sprintf(`{"initializedAt":"2026-01-01T00:00:00.000Z","lastTweetId":"%d","lastOkAt":"2020-01-01T00:00:00.000Z"}`, 100+i)
		if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,participation_mode,cursor,enabled,next_fetch_at)
			VALUES($1,$2,'x_search',$3,'editorial',$4,true,now()-interval '1 minute')`, id, "X shard test", config, cursor); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id = ANY($1)`, ids)
	})
	addCollectorSubscription(t, pool, ids...)
	var asked string
	posts := []tweet{}
	for i, handle := range handles {
		posts = append(posts, tweet{ID: fmt.Sprintf("%d", 900+i), CreatedAt: "2026-10-01T02:03:04Z", FullText: "Post " + handle, User: xUser{Name: handle, Handle: handle}})
	}
	provider := &scriptedXProvider{search: func(call xSearchCall) (XPage, error) {
		asked = call.query
		return XPage{Tweets: posts}, nil
	}}
	service := testCollectorService(orm, pool, nil)
	service.XProvider = provider
	if err := service.CollectXShard(ctx, "editorial:test-"+suffix, ids); err != nil {
		t.Fatal(err)
	}
	expected := "(from:" + handles[0] + " OR from:" + handles[1] + " OR from:" + handles[2] + ") -filter:replies since_id:"
	if !strings.HasPrefix(asked, expected) {
		t.Fatalf("shard query = %q, want prefix %q", asked, expected)
	}
	var assigned int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id = ANY($1)`, ids).Scan(&assigned); err != nil || assigned != len(ids) {
		t.Fatalf("articles assigned to shard sources = %d, error=%v", assigned, err)
	}
	for i, id := range ids {
		var handle string
		var count int
		if err := pool.QueryRow(ctx, `SELECT author FROM articles WHERE source_id=$1`, id).Scan(&handle); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT found_count FROM fetch_runs WHERE source_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if handle != handles[i] || count != 1 {
			t.Fatalf("source %s received author=%q and found=%d, want %q/1", id, handle, count, handles[i])
		}
	}
}

func TestXCoverageAndPaidReceiptCompleteInOneTransaction(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_x_atomic_%d", time.Now().UnixNano())
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,cursor,enabled,next_fetch_at)
		VALUES($1,'X atomic test','x_search','{"query":"from:atomic -filter:replies"}','{"lastTweetId":"100"}',true,now()-interval '1 minute')`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID) })
	addCollectorSubscription(t, pool, sourceID)
	var receiptID int64
	if err := pool.QueryRow(ctx, `INSERT INTO receipts(logical_key,service,purpose,subject,status,request,attempts)
		VALUES($1,'socialdata','source_fetch',$2,'received','{}',1) RETURNING id`, "x-atomic:"+sourceID, "source:"+sourceID).Scan(&receiptID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM receipts WHERE id=$1`, receiptID) })
	repository := NewRepository(orm, pool, config.SourceSchedule{})
	run, started, err := repository.BeginSourceRun(ctx, sourceID, true)
	if err != nil || !started {
		t.Fatalf("begin X fetch run = %v, %v", started, err)
	}
	defer repository.ReleaseSource(run)
	cursor := json.RawMessage(`{"lastTweetId":"200","initializedAt":"2026-10-01T00:00:00Z"}`)
	detail := json.RawMessage(`{"pages":1}`)
	trigger := fmt.Sprintf("fail_x_coverage_%d", time.Now().UnixNano())
	if err := orm.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.status = 'ok' THEN RAISE EXCEPTION 'injected X coverage failure'; END IF; RETURN NEW; END $$`, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON fetch_runs FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON fetch_runs`, trigger)).Error
		_ = orm.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, trigger)).Error
	})
	counts := map[string]int{sourceID: 1}
	err = repository.FinishXSuccess(ctx, []*sourceRun{run}, counts, counts, map[string]json.RawMessage{sourceID: cursor}, detail, []int64{receiptID})
	if err == nil {
		t.Fatal("injected fetch_runs failure was not returned")
	}
	var savedCursor json.RawMessage
	var receiptStatus, runStatus string
	if err := pool.QueryRow(ctx, `SELECT cursor FROM sources WHERE id=$1`, sourceID).Scan(&savedCursor); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM receipts WHERE id=$1`, receiptID).Scan(&receiptStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM fetch_runs WHERE id=$1`, run.RunID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if string(savedCursor) != `{"lastTweetId": "100"}` && string(savedCursor) != `{"lastTweetId":"100"}` || receiptStatus != "received" || runStatus != "running" {
		t.Fatalf("failed commit partially advanced state: cursor=%s receipt=%s run=%s", savedCursor, receiptStatus, runStatus)
	}
	if err := orm.Exec(fmt.Sprintf(`DROP TRIGGER %s ON fetch_runs`, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(fmt.Sprintf(`DROP FUNCTION %s()`, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishXSuccess(ctx, []*sourceRun{run}, counts, counts, map[string]json.RawMessage{sourceID: cursor}, detail, []int64{receiptID}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM receipts WHERE id=$1`, receiptID).Scan(&receiptStatus); err != nil || receiptStatus != "completed" {
		t.Fatalf("receipt after retry = %q, error=%v", receiptStatus, err)
	}
}

/** TestIsolatedFirstImportAdmitsTenFreshArticlesWithoutSecondFetchFlood verifies isolated cold-start slicing across runs. */
func TestIsolatedFirstImportAdmitsTenFreshArticlesWithoutSecondFetchFlood(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_isolated_%d", time.Now().UnixNano())
	address := "https://isolated.example.invalid/" + sourceID
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,participation_mode,config) VALUES($1,'Isolated RSS','rss','isolated',jsonb_build_object('feedUrl',$2::text))`, sourceID, address+"/feed"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)

	now := time.Now().UTC()
	var builder strings.Builder
	builder.WriteString(`<rss version="2.0"><channel><title>Isolated</title><link>`)
	builder.WriteString(address)
	builder.WriteString(`</link><description>Feed</description>`)
	for i := 0; i < 15; i++ {
		pub := now.Add(-time.Duration(i+1) * time.Hour).Format(time.RFC1123Z)
		builder.WriteString(fmt.Sprintf(`<item><title>Recent %d</title><link>%s/recent/%d</link><pubDate>%s</pubDate></item>`, i, address, i, pub))
	}
	oldPub := now.Add(-72 * time.Hour).Format(time.RFC1123Z)
	builder.WriteString(fmt.Sprintf(`<item><title>Old item</title><link>%s/old</link><pubDate>%s</pubDate></item>`, address, oldPub))
	builder.WriteString(fmt.Sprintf(`<item><title>Undated item</title><link>%s/undated</link></item>`, address))
	builder.WriteString(`</channel></rss>`)
	xmlPayload := builder.String()

	service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(xmlPayload))}, nil
	}))
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var liveCount, backfillCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT backfill), count(*) FILTER (WHERE backfill) FROM articles WHERE source_id=$1`, sourceID).Scan(&liveCount, &backfillCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 10 || backfillCount != 7 {
		t.Fatalf("first isolated fetch counts live=%d backfill=%d, want 10/7", liveCount, backfillCount)
	}

	if _, err := pool.Exec(ctx, `UPDATE sources SET reuse_window_minutes=0 WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var secondLive int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1 AND NOT backfill`, sourceID).Scan(&secondLive); err != nil {
		t.Fatal(err)
	}
	if secondLive != 10 {
		t.Fatalf("second fetch live count = %d, overflow leaked past backfill gate", secondLive)
	}
}
