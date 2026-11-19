package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/materials"
	publicationdomain "aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/receipts"
	"aihot/backend-go/internal/sourceschedule"
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
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || (!strings.HasSuffix(parsed.Path, "_test") && !strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("database must be localhost and end with _test or _ci")
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

/** TestMaterialSourceAttributionUsesPausedVerifiedPublisher verifies ownership correction independently of discovery. */
func TestMaterialSourceAttributionUsesPausedVerifiedPublisher(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	publisherID := "collector_publisher_" + suffix
	discoveryID := "collector_discovery_" + suffix
	articleURL := "https://publisher-" + suffix + ".example/news/release"
	configBytes, err := json.Marshal(map[string]any{"publisherUrlPrefixes": []string{"https://publisher-" + suffix + ".example/news"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config,enabled,health)
		VALUES($1,'Paused verified publisher','web_list','T1','editorial',$2::jsonb,false,'paused')`, publisherID, configBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config)
		VALUES($1,'Aggregator discovery','rss','T2','editorial','{}'::jsonb)`, discoveryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id IN ($1,$2)`, publisherID, discoveryID)
	})
	publicationRepository := publicationdomain.NewRepository(pool, nil, interests.PublicationSettings{}, "Test", "https://test.invalid")
	materialService := materials.NewService(materials.NewRepository(orm))
	materialService.SetSourceAttributor(materials.NewAttributionRepository(pool, publicationRepository))
	stored, err := materialService.Store(ctx, materials.Input{
		SourceID: discoveryID, URL: articleURL, Title: "Original title", Author: "Aggregator submitter",
		BodyText: "Original body", Via: "fetch",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+stored.ArticleID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE id=$1`, stored.ArticleID)
	})
	var sourceID string
	var author *string
	var title, body string
	var revision int
	if err := pool.QueryRow(ctx, `SELECT source_id,author,title,body_text,revision FROM articles WHERE id=$1`, stored.ArticleID).
		Scan(&sourceID, &author, &title, &body, &revision); err != nil {
		t.Fatal(err)
	}
	if sourceID != publisherID || author != nil || title != "Original title" || body != "Original body" || revision != 1 {
		t.Fatalf("attributed article source=%q author=%v title=%q body=%q revision=%d", sourceID, author, title, body, revision)
	}
	var discoveries, auditEntries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM article_discoveries WHERE article_id=$1 AND source_id=$2`, stored.ArticleID, discoveryID).Scan(&discoveries); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+stored.ArticleID).Scan(&auditEntries); err != nil {
		t.Fatal(err)
	}
	if discoveries != 1 || auditEntries != 1 {
		t.Fatalf("discovery rows=%d, attribution audit rows=%d", discoveries, auditEntries)
	}
	var publisherEnabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM sources WHERE id=$1`, publisherID).Scan(&publisherEnabled); err != nil {
		t.Fatal(err)
	}
	if publisherEnabled {
		t.Fatal("ownership correction enabled a paused publisher")
	}
}

/** TestMaterialSourceAttributionReopensAnUnjudgedSignal verifies promotion resets grouping without revising material. */
func TestMaterialSourceAttributionReopensAnUnjudgedSignal(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	signalID := "collector_signal_" + suffix
	publisherID := "collector_editorial_" + suffix
	articleURL := "https://publisher-" + suffix + ".example/news/release"
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config)
		VALUES($1,'Signal discovery','rss','T2','hot_signal','{}'::jsonb)`, signalID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id IN ($1,$2)`, signalID, publisherID)
	})
	publicationRepository := publicationdomain.NewRepository(pool, nil, interests.PublicationSettings{}, "Test", "https://test.invalid")
	materialService := materials.NewService(materials.NewRepository(orm))
	materialService.SetSourceAttributor(materials.NewAttributionRepository(pool, publicationRepository))
	first, err := materialService.Store(ctx, materials.Input{SourceID: signalID, URL: articleURL, Title: "Signal title", Author: "Signal author", BodyText: "Body", Via: "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+first.ArticleID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE id=$1`, first.ArticleID)
	})
	if err := pool.QueryRow(ctx, `UPDATE articles SET processing_state='skipped',grouping_status='complete',grouped_at=now(),selection_adds_value=false,selection_value_reason='signal judgement' WHERE id=$1 RETURNING id`, first.ArticleID).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	configBytes, err := json.Marshal(map[string]any{"publisherUrlPrefixes": []string{"https://publisher-" + suffix + ".example/news"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config,enabled,health)
		VALUES($1,'Editorial publisher','web_list','T1','editorial',$2::jsonb,false,'paused')`, publisherID, configBytes); err != nil {
		t.Fatal(err)
	}
	corrected, err := materialService.Store(ctx, materials.Input{SourceID: signalID, URL: articleURL, Title: "Signal title", Author: "Signal author", BodyText: "Body", Via: "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if corrected.ArticleID != first.ArticleID || corrected.Revised || !corrected.ProcessingNeeded {
		t.Fatalf("correction result = %#v", corrected)
	}
	var sourceID, state, groupingStatus string
	var revision int
	var selectionAddsValue *bool
	if err := pool.QueryRow(ctx, `SELECT source_id,revision,processing_state,grouping_status,selection_adds_value FROM articles WHERE id=$1`, first.ArticleID).
		Scan(&sourceID, &revision, &state, &groupingStatus, &selectionAddsValue); err != nil {
		t.Fatal(err)
	}
	if sourceID != publisherID || revision != 1 || state != "new" || groupingStatus != "pending" || selectionAddsValue != nil {
		t.Fatalf("promoted article source=%q revision=%d state=%q grouping=%q selection=%v", sourceID, revision, state, groupingStatus, selectionAddsValue)
	}
}

/** TestMaterialSourceAttributionRejectsAmbiguousPublishers avoids silently assigning an unverified owner. */
func TestMaterialSourceAttributionRejectsAmbiguousPublishers(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	discoveryID := "collector_ambiguous_discovery_" + suffix
	firstPublisherID := "collector_ambiguous_first_" + suffix
	secondPublisherID := "collector_ambiguous_second_" + suffix
	articleURL := "https://ambiguous-" + suffix + ".example/news/release"
	configBytes, err := json.Marshal(map[string]any{"publisherUrlPrefixes": []string{"https://ambiguous-" + suffix + ".example/news"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config)
		VALUES($1,'Signal discovery','rss','T2','hot_signal','{}'::jsonb),
		($2,'First publisher','web_list','T1','editorial',$3::jsonb),
		($4,'Second publisher','web_list','T1','editorial',$3::jsonb)`, discoveryID, firstPublisherID, configBytes, secondPublisherID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=ANY($1)`, []string{discoveryID, firstPublisherID, secondPublisherID})
	})
	publicationRepository := publicationdomain.NewRepository(pool, nil, interests.PublicationSettings{}, "Test", "https://test.invalid")
	materialService := materials.NewService(materials.NewRepository(orm))
	materialService.SetSourceAttributor(materials.NewAttributionRepository(pool, publicationRepository))
	stored, err := materialService.Store(ctx, materials.Input{SourceID: discoveryID, URL: articleURL, Title: "Signal report", Author: "Signal author", Via: "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+stored.ArticleID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE id=$1`, stored.ArticleID)
	})
	var sourceID string
	var author *string
	var revision, audits int
	if err := pool.QueryRow(ctx, `SELECT source_id,author,revision FROM articles WHERE id=$1`, stored.ArticleID).Scan(&sourceID, &author, &revision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+stored.ArticleID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if sourceID != discoveryID || author == nil || *author != "Signal author" || revision != 1 || audits != 0 {
		t.Fatalf("ambiguous ownership changed the material: source=%q author=%v revision=%d audits=%d", sourceID, author, revision, audits)
	}
}

/** TestMaterialSourceAttributionReusesCurrentJudgement preserves analyzed editorial state during correction. */
func TestMaterialSourceAttributionReusesCurrentJudgement(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	signalID := "collector_judged_signal_" + suffix
	publisherID := "collector_judged_publisher_" + suffix
	articleURL := "https://judged-" + suffix + ".example/news/release"
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config)
		VALUES($1,'Signal discovery','rss','T2','hot_signal','{}'::jsonb)`, signalID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=ANY($1)`, []string{signalID, publisherID})
	})
	publicationRepository := publicationdomain.NewRepository(pool, nil, interests.PublicationSettings{}, "Test", "https://test.invalid")
	materialService := materials.NewService(materials.NewRepository(orm))
	materialService.SetSourceAttributor(materials.NewAttributionRepository(pool, publicationRepository))
	input := materials.Input{SourceID: signalID, URL: articleURL, Title: "Judged report", Author: "Signal author", BodyText: "Original body", Via: "fetch"}
	first, err := materialService.Store(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+first.ArticleID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE id=$1`, first.ArticleID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config)
		VALUES($1,'Verified publisher','web_list','T1','editorial',$2::jsonb)`, publisherID,
		`{"publisherUrlPrefixes":["https://judged-`+suffix+`.example/news"]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analyses(article_id,input_revision,origin,relevance,category,tags,subjects,title_zh,summary_zh,score,selected,output)
		VALUES($1,1,'model','pass','ai-models',ARRAY['模型发布']::text[],ARRAY['openai']::text[],'模型发布','发布了模型。',90,true,'{}'::jsonb)`, first.ArticleID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE articles SET processing_state='analyzed',grouping_status='complete',grouped_at=now(),selection_adds_value=true,selection_value_reason='current judgement' WHERE id=$1`, first.ArticleID); err != nil {
		t.Fatal(err)
	}
	var beforeTimeline time.Time
	if err := pool.QueryRow(ctx, `SELECT timeline_at FROM articles WHERE id=$1`, first.ArticleID).Scan(&beforeTimeline); err != nil {
		t.Fatal(err)
	}
	corrected, err := materialService.Store(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var sourceID, title, body, state, grouping string
	var author, reason *string
	var revision int
	var selectionValue *bool
	var afterTimeline time.Time
	if err := pool.QueryRow(ctx, `SELECT source_id,author,title,body_text,revision,processing_state,grouping_status,selection_adds_value,selection_value_reason,timeline_at
		FROM articles WHERE id=$1`, first.ArticleID).Scan(&sourceID, &author, &title, &body, &revision, &state, &grouping, &selectionValue, &reason, &afterTimeline); err != nil {
		t.Fatal(err)
	}
	var analyses, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM analyses WHERE article_id=$1 AND input_revision=1`, first.ArticleID).Scan(&analyses); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+first.ArticleID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if corrected.Revised || corrected.ProcessingNeeded || sourceID != publisherID || author != nil || title != "Judged report" || body != "Original body" || revision != 1 || state != "analyzed" || grouping != "complete" || selectionValue == nil || !*selectionValue || reason == nil || *reason != "current judgement" || !afterTimeline.Equal(beforeTimeline) || analyses != 1 || audits != 1 {
		t.Fatalf("current judgement was not reused after attribution: result=%#v source=%q author=%v title=%q body=%q rev=%d state=%q group=%q selection=%v reason=%v timeline=%v/%v analyses=%d audits=%d",
			corrected, sourceID, author, title, body, revision, state, grouping, selectionValue, reason, beforeTimeline, afterTimeline, analyses, audits)
	}
}

/** addDetailRecoverySource creates a subscribed web list with bounded detail extraction. */
func addDetailRecoverySource(t *testing.T, pool *pgxpool.Pool, sourceID, address, mode string) {
	t.Helper()
	ctx := context.Background()
	configuration, err := json.Marshal(map[string]any{
		"url":          address,
		"itemSelector": "article",
		"linkSelector": "a",
		"detail": map[string]any{
			"maxFetches":          1,
			"titleSelector":       "h1",
			"titleAuthoritative":  true,
			"publishedAtSelector": "time",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,participation_mode,config,cursor)
		VALUES($1,'Detail recovery test','web_list',$2,$3::jsonb,'{}'::jsonb)`, sourceID, mode, configuration); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)
}

/** assertDetailAudit checks persisted recovery progress for the latest listing run. */
func assertDetailAudit(t *testing.T, pool *pgxpool.Pool, sourceID string, attempts, failures int, pending bool) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT detail FROM fetch_runs WHERE source_id=$1 ORDER BY id DESC LIMIT 1`, sourceID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Attempts int  `json:"detailAttempts"`
		Failures int  `json:"detailFailures"`
		Pending  bool `json:"detailPending"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.Attempts != attempts || audit.Failures != failures || audit.Pending != pending {
		t.Fatalf("detail recovery audit = %#v, want attempts=%d failures=%d pending=%t", audit, attempts, failures, pending)
	}
}

/** TestSuccessfulCollectionStartsNaming verifies that only real fetch success supplies an actor. */
func TestSuccessfulCollectionStartsNaming(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		manual    bool
		wantReady bool
	}{
		{name: "manual success", status: http.StatusOK, manual: true, wantReady: true},
		{name: "scheduled success", status: http.StatusOK, wantReady: true},
		{name: "fetch failure", status: http.StatusBadGateway, manual: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := collectorPool(t)
			orm := collectorORM(t)
			ctx := context.Background()
			id := fmt.Sprintf("collector_naming_%d", time.Now().UnixNano())
			const address = "https://example.invalid/feed"
			feedConfig := settings{"feedUrl": address}
			if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,cursor,next_fetch_at) VALUES
				($1,'个人RSS','rss',jsonb_build_object('feedUrl',$2::text),jsonb_build_object('rssURL',$2::text,'rssResponseURL',$2::text,'rssConfigHash',$3::text,'rssETag','v1','rssTitle','Saved channel'),now())`, id, address, rssConfigHash(feedConfig)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, id) })
			userID := addCollectorSubscription(t, pool, id)
			if _, err := pool.Exec(ctx, `UPDATE user_accounts SET verified_at=now() WHERE id=$1`, userID); err != nil {
				t.Fatal(err)
			}
			service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
				if tc.manual && (req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "") {
					t.Fatalf("forced request sent a stored validator")
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
		})
	}
}

func TestDueSourceIDsRequiresActiveSubscriptionForRSSAndWebLists(t *testing.T) {
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
	for _, kind := range []string{"rss", "web_list"} {
		if !got[ids[kind]] {
			t.Errorf("due-source scan omitted an actively subscribed %s source", kind)
		}
	}
	for _, kind := range []string{"json_list", "x_search"} {
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
	originalBody := strings.TrimSpace(strings.Repeat("original body ", 22))
	body := originalBody
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
	var etag, lastModified, cursorURL, responseURL, configHash, feedTitle string
	if err := pool.QueryRow(ctx, `SELECT cursor->>'rssETag',cursor->>'rssLastModified',cursor->>'rssURL',cursor->>'rssResponseURL',cursor->>'rssConfigHash',cursor->>'rssTitle' FROM sources WHERE id=$1`, sourceID).Scan(&etag, &lastModified, &cursorURL, &responseURL, &configHash, &feedTitle); err != nil {
		t.Fatal(err)
	}
	if etag != `"collector-v1"` || lastModified != "Wed, 01 Oct 2025 00:00:00 GMT" || cursorURL != address+"/feed" || responseURL != address+"/feed" || configHash != rssConfigHash(settings{"feedUrl": address + "/feed"}) || feedTitle != "Feed" {
		t.Fatalf("RSS metadata was not persisted: %q / %q / %q / %q / %q / %q", etag, lastModified, cursorURL, responseURL, configHash, feedTitle)
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
	markDue := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE sources SET next_fetch_at=now()-interval '1 second',
			last_ok_at=now()-interval '11 minutes' WHERE id=$1`, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	markDue()
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
	body = strings.TrimSpace(strings.Repeat("original bod� ", 22))
	markDue()
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var saved string
	if err := pool.QueryRow(ctx, `SELECT body_text FROM articles WHERE id=$1`, articleID).Scan(&saved); err != nil || saved != originalBody {
		t.Fatalf("replacement-character refresh changed body to %q: %v", saved, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE articles SET processing_state='analyzed', processing_attempts=4,
		grouping_status='complete', selection_adds_value=true WHERE id=$1`, articleID); err != nil {
		t.Fatal(err)
	}
	updatedBody := strings.TrimSpace(strings.Repeat("a longer updated source body ", 12))
	body = updatedBody
	markDue()
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var processingState, groupingStatus string
	var attempts, updatedRevision int
	var hasSelectionValue *bool
	if err := pool.QueryRow(ctx, `SELECT processing_state,processing_attempts,grouping_status,selection_adds_value,revision
		FROM articles WHERE id=$1`, articleID).Scan(&processingState, &attempts, &groupingStatus, &hasSelectionValue, &updatedRevision); err != nil {
		t.Fatal(err)
	}
	if processingState != "new" || attempts != 0 || groupingStatus != "pending" || hasSelectionValue != nil || updatedRevision != 2 {
		t.Fatalf("revision did not reset processing: state=%s attempts=%d grouping=%s selection=%v revision=%d", processingState, attempts, groupingStatus, hasSelectionValue, updatedRevision)
	}
	// A historical archive is kept read-only even if the source starts returning richer body text.
	if _, err := pool.Exec(ctx, `UPDATE articles SET created_at=(SELECT activated_at FROM personalization_rollout WHERE id=true)-interval '1 second' WHERE id=$1`, articleID); err != nil {
		t.Fatal(err)
	}
	body = strings.TrimSpace(strings.Repeat("different and much longer source body ", 12))
	markDue()
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT body_text FROM articles WHERE id=$1`, articleID).Scan(&saved); err != nil || saved != updatedBody {
		t.Fatalf("historical body = %q: %v", saved, err)
	}
}

/** TestMaterialsStorePreservesConcurrentRevisionHistory keeps identity, baseline, and revision semantics. */
func TestMaterialsStorePreservesConcurrentRevisionHistory(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("material_history_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	url := "https://materials.example.invalid/" + prefix
	identity := "url:" + url
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind) VALUES($1,'Material history test','rss')`, sourceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	service := materials.NewService(materials.NewRepository(orm))
	base := materials.Input{SourceID: sourceID, URL: url, IdentityKey: identity, Title: "T0", Excerpt: "e0", DiscoveredAt: time.Now().UTC(), Via: "fetch"}
	storeConcurrently := func(inputs []materials.Input) ([]materials.Result, []error) {
		t.Helper()
		results := make([]materials.Result, len(inputs))
		errorsByCall := make([]error, len(inputs))
		var wait sync.WaitGroup
		wait.Add(len(inputs))
		for index, input := range inputs {
			go func(index int, input materials.Input) {
				defer wait.Done()
				results[index], errorsByCall[index] = service.Store(ctx, input)
			}(index, input)
		}
		wait.Wait()
		return results, errorsByCall
	}

	firstInputs := make([]materials.Input, 6)
	for index := range firstInputs {
		firstInputs[index] = base
	}
	firstResults, firstErrors := storeConcurrently(firstInputs)
	articleID := ""
	created := 0
	for index, result := range firstResults {
		if firstErrors[index] != nil {
			t.Fatalf("concurrent initial report %d: %v", index, firstErrors[index])
		}
		if articleID == "" {
			articleID = result.ArticleID
		}
		if result.ArticleID != articleID {
			t.Fatalf("concurrent initial reports created different articles: %#v", firstResults)
		}
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("concurrent initial reports created %d articles, want one", created)
	}

	updates := make([]materials.Input, 6)
	for index := range updates {
		updates[index] = base
		updates[index].Title = fmt.Sprintf("T%d", index+1)
		updates[index].Excerpt = fmt.Sprintf("distinct excerpt %d", index+1)
	}
	updateResults, updateErrors := storeConcurrently(updates)
	for index, result := range updateResults {
		if updateErrors[index] != nil || result.ArticleID != articleID || !result.Revised {
			t.Fatalf("concurrent revision %d = %#v, err=%v", index, result, updateErrors[index])
		}
	}
	var articleRevision, historyCount int
	if err := pool.QueryRow(ctx, `SELECT revision FROM articles WHERE id=$1`, articleID).Scan(&articleRevision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM article_revisions WHERE article_id=$1`, articleID).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if articleRevision != 7 || historyCount != 7 {
		t.Fatalf("concurrent article revision/history = %d/%d, want 7/7", articleRevision, historyCount)
	}

	returned, err := service.Store(ctx, base)
	if err != nil || returned.Revised {
		t.Fatalf("return to a recorded version = %#v, %v", returned, err)
	}
	var afterReturn int
	if err := pool.QueryRow(ctx, `SELECT revision FROM articles WHERE id=$1`, articleID).Scan(&afterReturn); err != nil || afterReturn != 7 {
		t.Fatalf("return to an earlier version created revision %d: %v", afterReturn, err)
	}
	newVersion := base
	newVersion.Title = "T7"
	newVersion.Excerpt = "distinct excerpt 7"
	added, err := service.Store(ctx, newVersion)
	if err != nil || !added.Revised {
		t.Fatalf("new unseen version = %#v, %v", added, err)
	}

	importedID := prefix + "_imported"
	importedURL := url + "/imported"
	if _, err := pool.Exec(ctx, `INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,revision,content_hash,body_text,body_status,processing_state)
 VALUES($1,$2,$3,$4,'Imported title',now(),now(),1,NULL,'imported body','ok','analyzed')`, importedID, sourceID, "url:"+importedURL, importedURL); err != nil {
		t.Fatal(err)
	}
	imported := materials.Input{SourceID: sourceID, URL: importedURL, IdentityKey: "url:" + importedURL, Title: "Imported title", Excerpt: "feed summary", Via: "fetch"}
	baseline, err := service.Store(ctx, imported)
	if err != nil || baseline.ArticleID != importedID || baseline.Created || baseline.Revised {
		t.Fatalf("imported baseline = %#v, %v", baseline, err)
	}
	unchanged, err := service.Store(ctx, imported)
	if err != nil || unchanged.Revised {
		t.Fatalf("unchanged imported baseline = %#v, %v", unchanged, err)
	}
	imported.Title = "Imported title, corrected"
	corrected, err := service.Store(ctx, imported)
	if err != nil || !corrected.Revised {
		t.Fatalf("real change after imported baseline = %#v, %v", corrected, err)
	}
	var importedRevision, importedHistory int
	if err := pool.QueryRow(ctx, `SELECT revision FROM articles WHERE id=$1`, importedID).Scan(&importedRevision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM article_revisions WHERE article_id=$1`, importedID).Scan(&importedHistory); err != nil {
		t.Fatal(err)
	}
	if importedRevision != 2 || importedHistory != 2 {
		t.Fatalf("imported revision/history = %d/%d, want 2/2", importedRevision, importedHistory)
	}
}

/** TestDetailBudgetContinuesAfterKnownArticles checks stored detail progress across listings. */
func TestDetailBudgetContinuesAfterKnownArticles(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_detail_%d", time.Now().UnixNano())
	address := "https://detail.example.invalid/list"
	configuration, err := json.Marshal(map[string]any{
		"url":          address,
		"itemSelector": "article",
		"linkSelector": "a",
		"detail": map[string]any{
			"maxFetches":          3,
			"titleSelector":       "h1",
			"titleAuthoritative":  true,
			"publishedAtSelector": "time",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Detail test','web_list',$2::jsonb)`, sourceID, configuration); err != nil {
		t.Fatal(err)
	}
	addCollectorSubscription(t, pool, sourceID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	var detailRequests int
	service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/list" {
			var listing strings.Builder
			for i := 0; i < 10; i++ {
				fmt.Fprintf(&listing, `<article><a href="https://detail.example.invalid/article/%d">Read more</a></article>`, i)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(listing.String()))}, nil
		}
		detailRequests++
		body := fmt.Sprintf(`<html><body><article><h1>Recovered title %s</h1><time datetime="2026-11-17T10:00:00Z"></time><p>%s</p></article></body></html>`,
			strings.TrimPrefix(req.URL.Path, "/article/"), strings.Repeat("Verified original article body. ", 20))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if detailRequests != 3 {
		t.Fatalf("first run made %d detail requests, want the configured budget of 3", detailRequests)
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '11 minutes' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if detailRequests != 6 {
		t.Fatalf("second run made %d total detail requests, want the next three articles", detailRequests)
	}
	var recovered, revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE title LIKE 'Recovered title %'),
		(SELECT count(*) FROM article_revisions r JOIN articles a ON a.id=r.article_id WHERE a.source_id=$1)
		FROM articles WHERE source_id=$1`, sourceID).Scan(&recovered, &revisions); err != nil {
		t.Fatal(err)
	}
	if recovered != 6 || revisions != 13 {
		t.Fatalf("detail enrichment stored %d titles and %d revisions, want 6 and 13", recovered, revisions)
	}
	var audit json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT detail FROM fetch_runs WHERE source_id=$1 ORDER BY id DESC LIMIT 1`, sourceID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	var detailAudit map[string]any
	if err := json.Unmarshal(audit, &detailAudit); err != nil {
		t.Fatal(err)
	}
	if detailAudit["detailAttempts"] != float64(3) || detailAudit["detailFailures"] != float64(0) || detailAudit["detailPending"] != true {
		t.Fatalf("detail recovery audit = %#v", detailAudit)
	}
}

/** TestDetailFailureIsAuditedAndRecoveredWithoutSavingHotSignalBodies preserves retry and mode boundaries. */
func TestDetailFailureIsAuditedAndRecoveredWithoutSavingHotSignalBodies(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_detail_failure_%d", time.Now().UnixNano())
	address := "https://detail-failure.example.invalid/list"
	addDetailRecoverySource(t, pool, sourceID, address, "hot_signal")
	failing := true
	var detailRequests int
	service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/list" {
			listing := `<article><a href="https://detail-failure.example.invalid/article/0">Read more</a></article>`
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(listing))}, nil
		}
		detailRequests++
		if failing {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("Unavailable"))}, nil
		}
		published := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		body := fmt.Sprintf(`<article><h1>Recovered title</h1><time datetime="%s"></time></article>`, published)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	assertDetailAudit(t, pool, sourceID, 1, 1, true)
	if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '11 minutes' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	failing = false
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	assertDetailAudit(t, pool, sourceID, 1, 0, false)
	var recovered, dated, bodyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE title='Recovered title'),
		count(*) FILTER (WHERE published_at IS NOT NULL), count(*) FILTER (WHERE body_text IS NOT NULL)
		FROM articles WHERE source_id=$1`, sourceID).Scan(&recovered, &dated, &bodyCount); err != nil {
		t.Fatal(err)
	}
	if detailRequests != 2 || recovered != 1 || dated != 1 || bodyCount != 0 {
		t.Fatalf("detail recovery requests=%d titles=%d dated=%d bodyCount=%d", detailRequests, recovered, dated, bodyCount)
	}
}

/** TestUndatedDetailSuccessDoesNotConsumeFutureBudgets records successful extraction without rereads. */
func TestUndatedDetailSuccessDoesNotConsumeFutureBudgets(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_detail_undated_%d", time.Now().UnixNano())
	address := "https://detail-undated.example.invalid/list"
	addDetailRecoverySource(t, pool, sourceID, address, "editorial")
	var detailRequests int
	service := testCollectorService(orm, pool, fakeDoer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/list" {
			var listing strings.Builder
			for i := 0; i < 3; i++ {
				fmt.Fprintf(&listing, `<article><a href="https://detail-undated.example.invalid/article/%d">Read more</a></article>`, i)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(listing.String()))}, nil
		}
		detailRequests++
		body := fmt.Sprintf(`<article><h1>Undated title %s</h1><p>Article body</p></article>`, strings.TrimPrefix(req.URL.Path, "/article/"))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	for index := range 4 {
		if index > 0 {
			if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '11 minutes' WHERE id=$1`, sourceID); err != nil {
				t.Fatal(err)
			}
		}
		if err := service.CollectSourceNow(ctx, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	var recovered, dated int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE title LIKE 'Undated title %'),
		count(*) FILTER (WHERE published_at IS NOT NULL) FROM articles WHERE source_id=$1`, sourceID).Scan(&recovered, &dated); err != nil {
		t.Fatal(err)
	}
	if recovered != 3 || dated != 0 || detailRequests != 3 {
		t.Fatalf("undated recovery titles=%d dated=%d detail requests=%d, want 3/0/3", recovered, dated, detailRequests)
	}
}

/** TestJinaListingKeepsReceivedPageAcrossStorageFailure verifies paid response durability and receipt commit. */
func TestJinaListingKeepsReceivedPageAcrossStorageFailure(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_jina_listing_%d", time.Now().UnixNano())
	address := "https://r.jina.ai/https://example.org/news/"
	configuration, err := json.Marshal(map[string]any{
		"url":                  address,
		"allowUrlPrefixes":     []string{"https://example.org/news/"},
		"_aihot":               map[string]any{"initialBackfillLimit": 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,participation_mode,config,cursor)
		VALUES($1,'Jina listing test','web_list','editorial',$2::jsonb,'{}'::jsonb)`, sourceID, configuration); err != nil {
		t.Fatal(err)
	}
	addCollectorSubscription(t, pool, sourceID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM receipts WHERE subject=$1`, "source:"+sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	var calls int
	client := fakeDoer(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("Authorization") != "Bearer fixture-key" || request.Header.Get("X-Return-Format") != "markdown" {
			t.Fatalf("Jina headers = %#v", request.Header)
		}
		page := fmt.Sprintf("Title: News\nURL Source: https://example.org/news/\n\nMarkdown Content:\n[Research report %d](https://example.org/news/report-%d)\n", calls, calls)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(page))}, nil
	})
	service := testCollectorService(orm, pool, nil)
	service.Jina = NewJina("fixture-key", "https://jina.example", client, receipts.NewService(receipts.NewRepository(orm)))
	trigger := fmt.Sprintf("fail_jina_storage_%d", time.Now().UnixNano())
	if err := orm.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.source_id = '%s' THEN RAISE EXCEPTION 'injected Jina listing storage failure'; END IF;
		RETURN NEW; END $$`, trigger, sourceID)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON articles FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON articles`, trigger)).Error
		_ = orm.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, trigger)).Error
	})
	if _, err := service.CollectSourceJob(ctx, sourceID, true, 0); !errors.Is(err, ErrFetchRecorded) {
		t.Fatalf("failed storage run error = %v", err)
	}
	var receiptStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM receipts WHERE subject=$1 ORDER BY id DESC LIMIT 1`, "source:"+sourceID).Scan(&receiptStatus); err != nil || receiptStatus != "received" {
		t.Fatalf("receipt after storage failure = %q, error=%v", receiptStatus, err)
	}
	if err := orm.Exec(fmt.Sprintf(`DROP TRIGGER %s ON articles`, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(fmt.Sprintf(`DROP FUNCTION %s()`, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CollectSourceJob(ctx, sourceID, true, 0); err != nil {
		t.Fatal(err)
	}
	var articleCount, receiptCount int
	var completedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1`, sourceID).Scan(&articleCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='completed') FROM receipts WHERE subject=$1`, "source:"+sourceID).Scan(&receiptCount, &completedCount); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || articleCount != 1 || receiptCount != 2 || completedCount != 1 {
		t.Fatalf("Jina recovery calls=%d articles=%d receipts=%d completed=%d", calls, articleCount, receiptCount, completedCount)
	}
}

/** TestLaterCollectionRejectsUnseenArchiveAndRepairsKnownItems verifies the stored source window. */
func TestLaterCollectionRejectsUnseenArchiveAndRepairsKnownItems(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_admission_%d", time.Now().UnixNano())
	initializedAt := time.Now().UTC().Add(-7 * 24 * time.Hour).Truncate(time.Second)
	address := "https://admission.example.invalid/" + sourceID
	configJSON, _ := json.Marshal(map[string]string{"feedUrl": address + "/feed"})
	cursorJSON, _ := json.Marshal(map[string]string{"initializedAt": initializedAt.Format(time.RFC3339Nano)})
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,cursor,last_ok_at,next_fetch_at)
		VALUES($1,'Admission test','rss',$2::jsonb,$3::jsonb,now()-interval '30 minutes',now()-interval '1 minute')`, sourceID, string(configJSON), string(cursorJSON)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)
	service := testCollectorService(orm, pool, nil)
	knownURL := address + "/known"
	if _, err := service.Materials.Store(ctx, materials.Input{
		SourceID: sourceID, URL: knownURL, Title: "Unspecified date", Excerpt: "Before correction", Via: "fetch",
	}); err != nil {
		t.Fatal(err)
	}
	old := initializedAt.Add(-72 * time.Hour)
	boundary := initializedAt.Add(-48 * time.Hour)
	recent := initializedAt.Add(-24 * time.Hour)
	feed := fmt.Sprintf(`<rss version="2.0"><channel><title>Feed</title><link>%s</link><description>Feed</description>
		<item><title>Known correction</title><link>%s</link><pubDate>%s</pubDate><description>Corrected excerpt</description></item>
		<item><title>Unseen archive</title><link>%s/unseen</link><pubDate>%s</pubDate></item>
		<item><title>Boundary</title><link>%s/boundary</link><pubDate>%s</pubDate></item>
		<item><title>Recent</title><link>%s/recent</link><pubDate>%s</pubDate></item>
		<item><title>Undated</title><link>%s/undated</link></item>
		</channel></rss>`, address, knownURL, old.Format(time.RFC1123), address, old.Format(time.RFC1123), address, boundary.Format(time.RFC1123), address, recent.Format(time.RFC1123), address)
	service.Client = fakeDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(feed))}, nil
	})
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1`, sourceID).Scan(&count); err != nil || count != 4 {
		t.Fatalf("article count = %d, want known plus three admitted entries: %v", count, err)
	}
	var unseen int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE url=$1`, address+"/unseen").Scan(&unseen); err != nil || unseen != 0 {
		t.Fatalf("unseen archived article count = %d: %v", unseen, err)
	}
	var title, excerpt string
	var revision int
	if err := pool.QueryRow(ctx, `SELECT title,excerpt,revision FROM articles WHERE url=$1`, knownURL).Scan(&title, &excerpt, &revision); err != nil || title != "Known correction" || excerpt != "Corrected excerpt" || revision != 2 {
		t.Fatalf("known old article correction = %q/%q revision=%d: %v", title, excerpt, revision, err)
	}
	var savedInitialization string
	if err := pool.QueryRow(ctx, `SELECT cursor->>'initializedAt' FROM sources WHERE id=$1`, sourceID).Scan(&savedInitialization); err != nil || savedInitialization != initializedAt.Format(time.RFC3339Nano) {
		t.Fatalf("source initialization cursor changed to %q: %v", savedInitialization, err)
	}
}

/** TestInvalidPublicationClaimKeepsTheStoredTimeline preserves valid dates when a later claim is unusable. */
func TestInvalidPublicationClaimKeepsTheStoredTimeline(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_invalid_epoch_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Invalid epoch test','rss','{}')`, sourceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1`, sourceID)
	})

	discoveredAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	claimedAt := discoveredAt.Add(-time.Hour)
	service := materials.NewService(materials.NewRepository(orm))
	first, err := service.Store(ctx, materials.Input{
		SourceID:     sourceID,
		URL:          "https://example.invalid/" + sourceID,
		Title:        "Original title",
		PublishedAt:  &claimedAt,
		DiscoveredAt: discoveredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Store(ctx, materials.Input{
		SourceID:     sourceID,
		URL:          "https://example.invalid/" + sourceID,
		Title:        "Updated title",
		PublishedAt:  nil,
		DiscoveredAt: discoveredAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ArticleID != first.ArticleID || updated.Created || !updated.Revised {
		t.Fatalf("invalid-claim update = %#v, first insert = %#v", updated, first)
	}
	var publishedAt, publishedAtClaim, timelineAt time.Time
	var revision int
	if err := pool.QueryRow(ctx, `SELECT published_at,published_at_claim,timeline_at,revision FROM articles WHERE id=$1`, first.ArticleID).
		Scan(&publishedAt, &publishedAtClaim, &timelineAt, &revision); err != nil {
		t.Fatal(err)
	}
	if !publishedAt.Equal(claimedAt) || !publishedAtClaim.Equal(claimedAt) || !timelineAt.Equal(discoveredAt) || revision != 2 {
		t.Fatalf("stored timeline after invalid claim = %s/%s/%s revision %d", publishedAt, publishedAtClaim, timelineAt, revision)
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
	if calls != 0 || automaticStatus != "skipped" || !lastOKAfter.Equal(lastOKBefore) || !nextFetch.Equal(lastOKBefore.Add(time.Hour)) {
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

func TestManualReuseNeverExtendsTheRealFetchWindow(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_reuse_chain_%d", time.Now().UnixNano())
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,enabled,interval_minutes,reuse_window_minutes,last_ok_at,last_fetch_at,next_fetch_at)
		VALUES($1,'Reuse chain','rss','{"feedUrl":"https://example.invalid/chain.xml"}',true,30,60,
			now()-interval '7 minutes',now()-interval '7 minutes',now()+interval '23 minutes')`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID) })
	addCollectorSubscription(t, pool, sourceID)
	addCollectorSubscription(t, pool, sourceID)
	policy := config.SourceSchedule{
		AutoMinInterval:   15,
		ManualReuseWindow: 10,
		AutoReuseWindow:   10,
	}
	repo := NewRepository(orm, pool, policy)
	calls := 0
	service := NewService(repo, fakeDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`<rss version="2.0"><channel><title>Chain</title><link>https://example.invalid</link><description>Feed</description></channel></rss>`)),
		}, nil
	}), materials.NewService(materials.NewRepository(orm)))
	checkTimes := func() (time.Time, time.Time, time.Time) {
		t.Helper()
		var ok, fetched, next time.Time
		if err := pool.QueryRow(ctx, `SELECT last_ok_at,last_fetch_at,next_fetch_at FROM sources WHERE id=$1`, sourceID).Scan(&ok, &fetched, &next); err != nil {
			t.Fatal(err)
		}
		return ok, fetched, next
	}

	// Two callers within the first window may reuse the same recorded success.
	firstOK, firstFetch, firstNext := checkTimes()
	for range 2 {
		if err := service.CollectSourceNow(ctx, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	ok, fetched, next := checkTimes()
	if calls != 0 || !ok.Equal(firstOK) || !fetched.Equal(firstFetch) || !next.Equal(firstNext) {
		t.Fatalf("manual reuse changed real timestamps or schedule: calls=%d ok=%v fetched=%v next=%v", calls, ok, fetched, next)
	}

	// Even with two recent reuse records, the original upstream check has now expired.
	if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '10 minutes',last_fetch_at=now()-interval '10 minutes' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	ok, fetched, next = checkTimes()
	if calls != 1 || !ok.After(firstOK) || !fetched.Equal(ok) || !next.Equal(ok.Add(30*time.Minute)) {
		t.Fatalf("manual fetch failed to advance the plan: calls=%d ok=%v fetched=%v next=%v", calls, ok, fetched, next)
	}

	// The next click reuses the new upstream response. The old automatic slot stays deferred.
	if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=last_ok_at-interval '9 minutes',
		last_fetch_at=last_fetch_at-interval '9 minutes',next_fetch_at=next_fetch_at-interval '9 minutes' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	ok, fetched, next = checkTimes()
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	after, afterFetch, afterNext := checkTimes()
	due, err := repo.DueSourceIDs(ctx)
	if err != nil || calls != 1 || !after.Equal(ok) || !afterFetch.Equal(fetched) || !afterNext.Equal(next) || slices.Contains(due, sourceID) {
		t.Fatalf("reuse or scanning changed the new schedule: calls=%d due=%v err=%v", calls, due, err)
	}

	// A due job still checks the age of the real response against the automatic cap.
	if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '29 minutes',next_fetch_at=now() WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSource(ctx, sourceID); err != nil || calls != 2 {
		t.Fatalf("automatic window should require an upstream request: calls=%d err=%v", calls, err)
	}
}

func TestFetchCompletionUsesCurrentSubscriptions(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_schedule_%d", time.Now().UnixNano())
	feedURL := "https://example.invalid/schedule.xml"
	feedConfig := settings{"feedUrl": feedURL}
	_, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,cursor,enabled,next_fetch_at)
		VALUES($1,'Schedule','rss',jsonb_build_object('feedUrl',$2::text),jsonb_build_object('rssURL',$2::text,'rssResponseURL',$2::text,'rssConfigHash',$3::text,'rssETag','"schedule"'),true,now())`, sourceID, feedURL, rssConfigHash(feedConfig))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, sourceID) })
	userID := addCollectorSubscription(t, pool, sourceID)
	policy := config.SourceSchedule{
		AutoMinInterval:   15,
		ManualReuseWindow: 10,
		AutoReuseWindow:   10,
	}
	repo := NewRepository(orm, pool, policy)
	changeSubscription := func(enabled bool, minutes int) error {
		return orm.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`UPDATE user_source_subscriptions SET enabled=?,interval_minutes=? WHERE source_id=? AND user_id=?`,
				enabled, minutes, sourceID, userID).Error; err != nil {
				return err
			}
			return sourceschedule.Recompute(tx, sourceID, policy)
		})
	}
	markDue := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '2 hours',last_fetch_at=now()-interval '2 hours',next_fetch_at=now() WHERE id=$1`, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	checkDelay := func(want time.Duration) time.Time {
		t.Helper()
		var last, next time.Time
		if err := pool.QueryRow(ctx, `SELECT last_fetch_at,next_fetch_at FROM sources WHERE id=$1`, sourceID).Scan(&last, &next); err != nil {
			t.Fatal(err)
		}
		if got := next.Sub(last); got != want {
			t.Fatalf("next scheduled check after %v, want %v", got, want)
		}
		return next
	}
	runs := 0
	service := NewService(repo, fakeDoer(func(req *http.Request) (*http.Response, error) {
		runs++
		if (runs == 1 || runs == 4) && req.Header.Get("If-None-Match") != `"schedule"` {
			t.Fatalf("scheduled check %d omitted its valid ETag", runs)
		}
		switch runs {
		case 1:
			if err := changeSubscription(true, 5); err != nil {
				t.Fatal(err)
			}
		case 2:
			if err := changeSubscription(true, 20); err != nil {
				t.Fatal(err)
			}
			return nil, fmt.Errorf("automatic check failed")
		case 3:
			return nil, fmt.Errorf("manual check failed")
		case 4:
			if err := changeSubscription(false, 20); err != nil {
				t.Fatal(err)
			}
		}
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}), materials.NewService(materials.NewRepository(orm)))
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	checkDelay(15 * time.Minute)
	markDue()
	if err := service.CollectSource(ctx, sourceID); !errors.Is(err, ErrFetchRecorded) {
		t.Fatalf("automatic failure = %v", err)
	}
	beforeManual := checkDelay(40 * time.Minute)
	if err := service.CollectSourceNow(ctx, sourceID); !errors.Is(err, ErrFetchRecorded) {
		t.Fatalf("manual failure = %v", err)
	}
	var next *time.Time
	if err := pool.QueryRow(ctx, `SELECT next_fetch_at FROM sources WHERE id=$1`, sourceID).Scan(&next); err != nil || next == nil || !next.Equal(beforeManual) {
		t.Fatalf("manual failure changed the schedule to %v: %v", next, err)
	}
	markDue()
	if err := service.CollectSource(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled,next_fetch_at FROM sources WHERE id=$1`, sourceID).Scan(&enabled, &next); err != nil || enabled || next != nil {
		t.Fatalf("finished fetch scheduled an inactive source: enabled=%v next=%v err=%v", enabled, next, err)
	}
	if err := changeSubscription(true, 20); err != nil {
		t.Fatal(err)
	}
	checkDelay(15 * time.Minute)
	due, err := repo.DueSourceIDs(ctx)
	if err != nil || slices.Contains(due, sourceID) {
		t.Fatalf("recently reactivated source bypassed the platform minimum: due=%v err=%v", due, err)
	}
	markDue()
	due, err = repo.DueSourceIDs(ctx)
	if err != nil || !slices.Contains(due, sourceID) {
		t.Fatalf("reactivated source was not picked up by the scanner: due=%v err=%v", due, err)
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

/** TestXSearchResumesToItsSavedWatermarkAcrossRuns preserves every post in a paginated stretch. */
func TestXSearchResumesToItsSavedWatermarkAcrossRuns(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_x_backlog_%d", time.Now().UnixNano())
	const baseID uint64 = 1_000_000_000_000_000_000
	watermark := baseID + 100
	cursorJSON := fmt.Sprintf(`{"initializedAt":"2026-10-01T00:00:00Z","lastTweetId":"%d"}`, watermark)
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config,cursor,enabled,next_fetch_at)
		VALUES($1,'X backlog test','x_search','T1','editorial','{"query":"from:demo -filter:replies","searchType":"Latest"}',$2::jsonb,true,now()-interval '1 minute')`, sourceID, cursorJSON); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)
	posts := make([]tweet, 450)
	for index := range posts {
		id := baseID + 550 - uint64(index)
		posts[index] = tweet{
			ID:        strconv.FormatUint(id, 10),
			CreatedAt: time.Now().Add(-time.Duration(index) * time.Hour).Format(time.RFC3339),
			FullText:  fmt.Sprintf("Post %d", id),
			Lang:      "en",
			User:      xUser{Name: "Demo", Handle: "demo"},
		}
	}
	provider := &scriptedXProvider{search: func(call xSearchCall) (XPage, error) {
		marker := "since_id:"
		position := strings.LastIndex(call.query, marker)
		if position < 0 {
			t.Fatalf("X query lost the stored watermark: %q", call.query)
		}
		queryWatermark, err := strconv.ParseUint(call.query[position+len(marker):], 10, 64)
		if err != nil {
			t.Fatalf("parse query watermark from %q: %v", call.query, err)
		}
		eligible := make([]tweet, 0, len(posts))
		for _, post := range posts {
			id, err := strconv.ParseUint(post.ID, 10, 64)
			if err == nil && id > queryWatermark {
				eligible = append(eligible, post)
			}
		}
		pageIndex := 0
		if call.cursor != "" {
			pageIndex, err = strconv.Atoi(call.cursor)
			if err != nil {
				t.Fatalf("parse page cursor %q: %v", call.cursor, err)
			}
		}
		start := pageIndex * 20
		if start >= len(eligible) {
			return XPage{}, nil
		}
		end := min(start+20, len(eligible))
		page := XPage{Tweets: eligible[start:end]}
		if end < len(eligible) {
			page.NextCursor = strconv.Itoa(pageIndex + 1)
		}
		return page, nil
	}}
	service := testCollectorService(orm, pool, nil)
	service.XProvider = provider
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1`, sourceID).Scan(&stored); err != nil || stored != 400 {
		t.Fatalf("first collection stored %d posts, want 400: %v", stored, err)
	}
	var cursor json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT cursor FROM sources WHERE id=$1`, sourceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(cursor, &saved); err != nil || saved["lastTweetId"] != strconv.FormatUint(baseID+550, 10) {
		t.Fatalf("first collection watermark = %s, err=%v", cursor, err)
	}
	var backlog []XBacklog
	if raw, ok := saved["xBacklog"]; !ok || json.Unmarshal(mustJSONValue(raw), &backlog) != nil || len(backlog) != 1 {
		t.Fatalf("first collection did not persist the unread stretch: %s", cursor)
	}
	var detail json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT detail FROM fetch_runs WHERE source_id=$1 ORDER BY id DESC LIMIT 1`, sourceID).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var firstRun map[string]any
	if err := json.Unmarshal(detail, &firstRun); err != nil || firstRun["truncated"] != true || firstRun["backlog"] != float64(1) {
		t.Fatalf("first collection backlog audit = %s, err=%v", detail, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '11 minutes' WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectSourceNow(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1`, sourceID).Scan(&stored); err != nil || stored != len(posts) {
		t.Fatalf("second collection stored %d posts, want %d: %v", stored, len(posts), err)
	}
	if len(provider.calls) != 24 {
		t.Fatalf("provider pages requested across two runs = %d, want 24 without re-fetches", len(provider.calls))
	}
	if err := pool.QueryRow(ctx, `SELECT cursor FROM sources WHERE id=$1`, sourceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	saved = nil
	if err := json.Unmarshal(cursor, &saved); err != nil {
		t.Fatal(err)
	}
	if _, exists := saved["xBacklog"]; exists {
		t.Fatalf("completed stretch remained in the source cursor: %s", cursor)
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

	if _, err := pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '11 minutes' WHERE id=$1`, sourceID); err != nil {
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

/** TestRSSTailCommitsBefore304AndRetainsOldValidatorAfterStorageFailure preserves the listing tail contract. */
func TestRSSTailCommitsBefore304AndRetainsOldValidatorAfterStorageFailure(t *testing.T) {
	pool := collectorPool(t)
	orm := collectorORM(t)
	ctx := context.Background()
	sourceID := fmt.Sprintf("collector_rss_tail_%d", time.Now().UnixNano())
	address := "https://rss-tail.example.invalid/" + sourceID
	initializedAt := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config,cursor)
		VALUES($1,'RSS tail test','rss',jsonb_build_object('feedUrl',$2::text,'_aihot',jsonb_build_object('initialBackfillLimit',61)),
		jsonb_build_object('initializedAt',$3::text,'rssURL',$2::text,'rssResponseURL',$2::text,'rssConfigHash',$4::text,'rssETag','"old"'))`, sourceID, address, initializedAt, rssConfigHash(settings{"feedUrl": address, "_aihot": map[string]any{"initialBackfillLimit": 61}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1`, sourceID)
	})
	addCollectorSubscription(t, pool, sourceID)

	trigger := fmt.Sprintf("fail_rss_tail_%d", time.Now().UnixNano())
	function := fmt.Sprintf("%s_fn", trigger)
	if err := orm.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.source_id = '%s' AND NEW.title = 'Tail item 61' THEN RAISE EXCEPTION 'injected RSS tail storage failure'; END IF;
		RETURN NEW; END $$`, function, sourceID)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON articles FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, function)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orm.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON articles`, trigger)).Error
		_ = orm.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, function)).Error
	})

	var requests []string
	service := testCollectorService(orm, pool, fakeDoer(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Header.Get("If-None-Match"))
		if len(requests) == 3 {
			return &http.Response{StatusCode: http.StatusNotModified, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		var feed strings.Builder
		feed.WriteString(`<rss version="2.0"><channel><title>Tail feed</title><link>` + address + `</link><description>Feed</description>`)
		published := time.Now().UTC().Format(time.RFC1123)
		for index := 1; index <= 61; index++ {
			fmt.Fprintf(&feed, `<item><title>Tail item %d</title><link>%s/item-%d</link><pubDate>%s</pubDate><description>Summary %d</description></item>`, index, address, index, published, index)
		}
		feed.WriteString(`</channel></rss>`)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": []string{`"new"`}}, Body: io.NopCloser(strings.NewReader(feed.String()))}, nil
	}))

	if _, err := service.CollectSourceJob(ctx, sourceID, false, 0); !errors.Is(err, ErrFetchRecorded) {
		t.Fatalf("tail storage failure = %v, want recorded fetch error", err)
	}
	markDue := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE sources SET next_fetch_at=now()-interval '1 second',last_ok_at=now()-interval '11 minutes' WHERE id=$1`, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1`, sourceID).Scan(&count); err != nil || count != 60 {
		t.Fatalf("stored tail before retry = %d, want 60: %v", count, err)
	}
	var etag string
	if err := pool.QueryRow(ctx, `SELECT cursor->>'rssETag' FROM sources WHERE id=$1`, sourceID).Scan(&etag); err != nil || etag != `"old"` {
		t.Fatalf("validator advanced after partial storage: %q, %v", etag, err)
	}
	if err := orm.Exec(fmt.Sprintf(`DROP TRIGGER %s ON articles`, trigger)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(fmt.Sprintf(`DROP FUNCTION %s()`, function)).Error; err != nil {
		t.Fatal(err)
	}
	markDue()
	if _, err := service.CollectSourceJob(ctx, sourceID, false, 0); err != nil {
		t.Fatal(err)
	}
	markDue()
	if _, err := service.CollectSourceJob(ctx, sourceID, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM articles WHERE source_id=$1`, sourceID).Scan(&count); err != nil || count != 61 {
		t.Fatalf("stored tail after retry and 304 = %d, want 61: %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT cursor->>'rssETag' FROM sources WHERE id=$1`, sourceID).Scan(&etag); err != nil || etag != `"new"` {
		t.Fatalf("successful retry did not advance validator: %q, %v", etag, err)
	}
	if len(requests) != 3 || requests[0] != `"old"` || requests[1] != `"old"` || requests[2] != `"new"` {
		t.Fatalf("conditional validators = %#v, want old/old/new", requests)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM article_revisions r JOIN articles a ON a.id=r.article_id WHERE a.source_id=$1`, sourceID).Scan(&revisions); err != nil || revisions != 61 {
		t.Fatalf("revisions after retry = %d, want one per accepted item: %v", revisions, err)
	}
}
