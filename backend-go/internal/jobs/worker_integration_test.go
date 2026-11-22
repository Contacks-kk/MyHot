package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"aihot/backend-go/internal/articlebody"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/contentpipeline"
	"aihot/backend-go/internal/db"
	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/materials"
	"aihot/backend-go/internal/modelconfig"
	"aihot/backend-go/internal/personal"
	"aihot/backend-go/internal/provider"
	publicationdomain "aihot/backend-go/internal/publication"
	"aihot/backend-go/internal/receipts"
	"aihot/backend-go/internal/sourcenaming"
	"aihot/backend-go/internal/tagging"
	"aihot/backend-go/internal/topics"
	"aihot/backend-go/internal/usersources"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"sync/atomic"
)

func localTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("integration database must be localhost and end with _test")
	}
	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func localTestORM(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	orm, err := gorm.Open(postgres.Open(raw), &gorm.Config{})
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

/** TestSourceNamingKeepsPersonalAliasesAndUsesTheCorrectModel covers both model owners. */
func TestSourceNamingKeepsPersonalAliasesAndUsesTheCorrectModel(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	subscriptions := usersources.NewService(f.orm, config.SourceSchedule{})
	created, err := subscriptions.Create(ctx, f.users[0], usersources.Input{
		Name: "Private alias",
		URL:  "https://naming.example.invalid/news/" + f.prefix + ".xml?token=secret-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	followed, err := subscriptions.SubscribeExisting(ctx, f.users[1], usersources.SubscribeExistingInput{SourceID: created.SourceID})
	if err != nil || !followed.NameInherited || followed.Name == created.Name || strings.Contains(followed.Name, "secret-value") {
		t.Fatalf("default subscription=%#v, err=%v", followed, err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM user_source_subscriptions WHERE source_id=$1`, created.SourceID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, created.SourceID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM receipts WHERE purpose='source_naming' AND subject=$1`, f.source)
	})
	if err := f.orm.Transaction(func(tx *gorm.DB) error {
		if err := sourcenaming.RecordCollection(tx, created.SourceID, f.users[1]); err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE sources SET name='个人 RSS' WHERE id=?`, f.source).Error; err != nil {
			return err
		}
		return sourcenaming.RecordCollection(tx, f.source, 0)
	}); err != nil {
		t.Fatal(err)
	}
	var userCalls, fallbackCalls atomic.Int32
	const canonical = "Naming test news"
	chat := func(_ context.Context, cfg provider.Config, prompt string) (provider.Completion, error) {
		if strings.Contains(prompt, "secret-value") || strings.Contains(prompt, "Private alias") {
			return provider.Completion{}, fmt.Errorf("private value in naming evidence")
		}
		if cfg.Model == "model-0" {
			userCalls.Add(1)
		} else if cfg.Model == "developer-model" {
			fallbackCalls.Add(1)
		} else {
			return provider.Completion{}, fmt.Errorf("unexpected model %q", cfg.Model)
		}
		return provider.Completion{Text: `{"name":"` + canonical + `"}`}, nil
	}
	repository := sourcenaming.NewRepository(f.orm)
	paid := receipts.NewService(receipts.NewRepository(f.orm))
	withoutFallback := sourcenaming.NewService(repository, paid, f.cfg, chat)
	if ids, err := withoutFallback.ReadyIDs(ctx, f.source); err != nil || len(ids) != 0 {
		t.Fatalf("legacy automatic source became ready without fallback: %v, %v", ids, err)
	}
	if err := withoutFallback.Name(ctx, f.source); err != nil {
		t.Fatal(err)
	}

	f.cfg.SourceNamingBaseURL = "https://fallback.example.invalid/v1"
	f.cfg.SourceNamingModel = "developer-model"
	f.cfg.SourceNamingAPIKey = "fixture-key"
	service := sourcenaming.NewService(repository, paid, f.cfg, chat)
	if ids, err := service.ReadyIDs(ctx, f.source); err != nil || len(ids) != 1 {
		t.Fatalf("configured legacy fallback not ready: %v, %v", ids, err)
	}
	results := make(chan error, 3)
	for _, id := range []string{created.SourceID, f.source, created.SourceID} {
		go func() { results <- service.Name(ctx, id) }()
	}
	for range 3 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if userCalls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("model calls user=%d fallback=%d", userCalls.Load(), fallbackCalls.Load())
	}
	var shared, legacy string
	if err := f.pool.QueryRow(ctx, `SELECT a.name,b.name FROM sources a CROSS JOIN sources b WHERE a.id=$1 AND b.id=$2`, created.SourceID, f.source).Scan(&shared, &legacy); err != nil {
		t.Fatal(err)
	}
	if shared == legacy || !strings.HasPrefix(shared, canonical) || !strings.HasPrefix(legacy, canonical) {
		t.Fatalf("shared names collided: %q / %q", shared, legacy)
	}
	if own, err := subscriptions.Get(ctx, f.users[0], created.ID); err != nil || own.Name != "Private alias" || own.NameInherited {
		t.Fatalf("private name changed: %#v, %v", own, err)
	}
	if inherited, err := subscriptions.Get(ctx, f.users[1], followed.ID); err != nil || inherited.Name != shared || !inherited.NameInherited {
		t.Fatalf("inherited name did not update: %#v, %v", inherited, err)
	}
	inheritName := true
	restored, err := subscriptions.Update(ctx, f.users[0], created.ID, usersources.Input{
		NameInherited:      &inheritName,
		URL:                created.URL,
		IntervalMinutes:    created.IntervalMinutes,
		ReuseWindowMinutes: created.ReuseWindowMinutes,
	})
	if err != nil || restored.Name != shared || !restored.NameInherited {
		t.Fatalf("restore shared name=%#v, err=%v", restored, err)
	}
	var receiptOwner int64
	if err := f.pool.QueryRow(ctx, `SELECT user_id FROM user_model_call_receipts WHERE logical_key=$1 AND purpose='source_naming' AND status='completed'`, "source_naming:"+created.SourceID).Scan(&receiptOwner); err != nil || receiptOwner != f.users[0] {
		t.Fatalf("personal receipt owner=%d, err=%v", receiptOwner, err)
	}
	var platformStatus string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM receipts WHERE subject=$1 AND purpose='source_naming'`, f.source).Scan(&platformStatus); err != nil || platformStatus != "completed" {
		t.Fatalf("platform receipt=%q, err=%v", platformStatus, err)
	}
}

type workerFixture struct {
	pool           *pgxpool.Pool
	orm            *gorm.DB
	source, prefix string
	users          [2]int64
	cfg            config.Config
	ciphertext     [2]string
}

func fixture(t *testing.T) workerFixture {
	t.Helper()
	pool := localTestPool(t)
	orm := localTestORM(t)
	prefix := fmt.Sprintf("worker_%d", time.Now().UnixNano())
	f := workerFixture{pool: pool, orm: orm, source: prefix + "_source", prefix: prefix, cfg: config.Config{SiteName: "Test", SiteURL: "https://test.invalid", PersonalizationEnabled: true, ModelCallsEnabled: true, UserJobMaxWorkers: 4, UserAPIKeyEncryptionKey: []byte(strings.Repeat("s", 32))}}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Worker test','rss','{"feedUrl":"https://worker.example.invalid/feed"}')`, f.source); err != nil {
		t.Fatal(err)
	}
	for i, keyword := range []string{"OpenAI", "Anthropic"} {
		if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash,verified_at,created_at) VALUES($1,'unused',now()-interval '3 days',now()-interval '3 days') RETURNING id`, fmt.Sprintf("%s_%d@example.invalid", prefix, i)).Scan(&f.users[i]); err != nil {
			t.Fatal(err)
		}
		encrypted, err := modelconfig.Encrypt(f.cfg.UserAPIKeyEncryptionKey, []byte(fmt.Sprintf("private-test-key-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		f.ciphertext[i] = encrypted
		if _, err := pool.Exec(ctx, `INSERT INTO user_model_configs(user_id,base_url,api_key_ciphertext,model,updated_at) VALUES($1,'https://model.example.invalid/v1',$2,$3,now()-interval '2 days')`, f.users[i], encrypted, fmt.Sprintf("model-%d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_interests(user_id,keywords,updated_at) VALUES($1,$2,now()-interval '2 days')`, f.users[i], []string{keyword}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_source_subscriptions(user_id,source_id,name,activated_at) VALUES($1,$2,'Worker test',now()-interval '2 days')`, f.users[i], f.source); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE id=ANY($1)`, f.users[:])
		_, _ = pool.Exec(ctx, `DELETE FROM articles WHERE source_id=$1`, f.source)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, f.source)
	})
	return f
}

func (f workerFixture) article(t *testing.T, suffix, title string, created time.Time) string {
	t.Helper()
	id := f.prefix + "_" + suffix
	_, err := f.pool.Exec(context.Background(), `INSERT INTO articles(id,source_id,identity_key,url,title,excerpt,discovered_at,timeline_at,created_at) VALUES($1,$2,$1,$3,$4,$4,$5,$5,$5)`, id, f.source, "https://articles.example.invalid/"+id, title, created)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO article_discoveries(article_id,source_id,via,discovered_at) VALUES($1,$2,'fetch',$3)`, id, f.source, created); err != nil {
		t.Fatal(err)
	}
	return id
}

func articleJob(userID int64, id string, modelVersion, preferenceVersion int) *river.Job[ArticleArgs] {
	return &river.Job[ArticleArgs]{JobRow: &rivertype.JobRow{ID: time.Now().UnixNano(), Attempt: 1}, Args: ArticleArgs{UserID: userID, ArticleID: id, Revision: 1, ModelVersion: modelVersion, PreferenceVersion: preferenceVersion}}
}

/** TestColdStartSubscriptionSlicesTenArticlesOnce verifies late subscribers receive a single <=10 cold-start slice. */
func TestColdStartSubscriptionSlicesTenArticlesOnce(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	repo := NewRepository(f.orm, f.pool)
	if err := repo.EnsureRollout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sources SET last_ok_at=now()-interval '2 hours' WHERE id=$1`, f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE user_source_subscriptions SET cold_start_pending=true, activated_at=now() WHERE user_id=$1 AND source_id=$2`, f.users[0], f.source); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		f.article(t, fmt.Sprintf("cs_%02d", i), fmt.Sprintf("OpenAI cold start item %d", i), time.Now().Add(-time.Duration(30+i)*time.Minute))
	}
	if _, err := f.pool.Exec(ctx, `UPDATE articles SET created_at=now() WHERE source_id=$1`, f.source); err != nil {
		t.Fatal(err)
	}

	var queuedArticles []ArticleArgs
	var queuedTriages []TriageBatchArgs
	service := NewService(repo, f.cfg, nil)
	if err := service.ScanWithTriage(
		ctx,
		func(_ context.Context, _ pgx.Tx, args ArticleArgs) error {
			queuedArticles = append(queuedArticles, args)
			return nil
		},
		func(_ context.Context, _ pgx.Tx, args TriageBatchArgs) error {
			queuedTriages = append(queuedTriages, args)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(queuedArticles) != 0 || len(queuedTriages) != 1 || len(queuedTriages[0].Items) != 10 {
		t.Fatalf("cold-start dispatches = %d articles / %d triages, want 0/1(10)", len(queuedArticles), len(queuedTriages))
	}
	for _, item := range queuedTriages[0].Items {
		if !item.ColdStart {
			t.Fatalf("expected ColdStart flag on sliced item: %+v", item)
		}
	}

	var pending bool
	if err := f.pool.QueryRow(ctx, `SELECT cold_start_pending FROM user_source_subscriptions WHERE user_id=$1 AND source_id=$2`, f.users[0], f.source).Scan(&pending); err != nil || pending {
		t.Fatalf("cold_start_pending = %t, err=%v", pending, err)
	}
	queuedArticles = nil
	queuedTriages = nil
	if err := service.ScanWithTriage(
		ctx,
		func(_ context.Context, _ pgx.Tx, args ArticleArgs) error {
			queuedArticles = append(queuedArticles, args)
			return nil
		},
		func(_ context.Context, _ pgx.Tx, args TriageBatchArgs) error {
			queuedTriages = append(queuedTriages, args)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(queuedArticles) != 0 || len(queuedTriages) != 0 {
		t.Fatalf("second scan re-dispatched cold-start slice: articles=%d triages=%d", len(queuedArticles), len(queuedTriages))
	}
}

/** TestBatchTriageFunnelSelectsTopFive verifies Stage 2 batch triage and Stage 3 handoff. */
func TestBatchTriageFunnelSelectsTopFive(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	candidates := make([]TriageCandidate, 0, 8)
	for i := 0; i < 8; i++ {
		id := f.article(t, fmt.Sprintf("tr_%d", i), fmt.Sprintf("OpenAI article %d", i), time.Now().Add(-time.Hour))
		candidates = append(candidates, TriageCandidate{
			ArticleID: id,
			Revision:  1,
			SourceID:  f.source,
			Title:     fmt.Sprintf("OpenAI article %d", i),
			Snippet:   "Brief summary",
		})
	}
	var calls int
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, func(_ context.Context, _ provider.Config, _ string) (provider.Completion, error) {
		calls++
		return provider.Completion{Text: `{"selected":[2,5,7]}`, Usage: provider.Usage{TotalTokens: 45}}, nil
	})
	var enqueued []ArticleArgs
	err := service.ProcessTriageBatch(ctx, TriageBatchArgs{
		UserID:            f.users[0],
		ModelVersion:      1,
		PreferenceVersion: 1,
		BatchKey:          "batch-test-1",
		Items:             candidates,
	}, 9001, 1, func(_ context.Context, args ArticleArgs) error {
		enqueued = append(enqueued, args)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(enqueued) != 3 {
		t.Fatalf("triage calls=%d enqueued=%d, want 1/3", calls, len(enqueued))
	}
	if enqueued[0].ArticleID != candidates[1].ArticleID || enqueued[1].ArticleID != candidates[4].ArticleID || enqueued[2].ArticleID != candidates[6].ArticleID {
		t.Fatalf("selected articles mismatch: %+v", enqueued)
	}
	var receiptCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_model_call_receipts WHERE user_id=$1 AND purpose='article' AND article_id IS NULL AND status='completed'`, f.users[0]).Scan(&receiptCount); err != nil || receiptCount != 1 {
		t.Fatalf("triage receipt count = %d, %v", receiptCount, err)
	}
}

/** TestUserDailyModelCallLimitStopsExtraCalls enforces USER_DAILY_MODEL_CALL_LIMIT per user day. */
func TestUserDailyModelCallLimitStopsExtraCalls(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.cfg.UserDailyModelCallLimit = 2
	var calls int
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, func(_ context.Context, _ provider.Config, _ string) (provider.Completion, error) {
		calls++
		return provider.Completion{Text: `{"relevanceScore":88,"title":"热点","summary":"摘要内容","reason":"命中"}`, Usage: provider.Usage{TotalTokens: 10}}, nil
	})
	worker := ArticleWorker{Service: service, Enabled: true}
	for i := 0; i < 3; i++ {
		id := f.article(t, fmt.Sprintf("quota_%d", i), fmt.Sprintf("OpenAI quota test %d", i), time.Now().Add(-time.Hour))
		if err := worker.Work(ctx, articleJob(f.users[0], id, 1, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("daily quota calls = %d, want 2", calls)
	}
}

func TestPersonalArticleWorkerIsolationDedupAndSettingsBoundary(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	a := f.article(t, "openai", "OpenAI announces a model", time.Now().Add(-time.Hour))
	b := f.article(t, "anthropic", "Anthropic announces a model", time.Now().Add(-time.Hour))
	unrelated := f.article(t, "unrelated", "Weather forecast", time.Now().Add(-time.Hour))
	stale := f.article(t, "stale", "OpenAI imported old release", time.Now().Add(-time.Hour))
	if _, err := f.pool.Exec(ctx, `UPDATE articles SET published_at=discovered_at-interval '49 hours' WHERE id=$1`, stale); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, func(_ context.Context, cfg provider.Config, _ string) (provider.Completion, error) {
		calls = append(calls, cfg.Model)
		if cfg.APIKey != "private-test-key-0" && cfg.APIKey != "private-test-key-1" {
			t.Fatal("wrong private model credential")
		}
		return provider.Completion{Text: `{"relevanceScore":91,"title":"个人热点","summary":"模型发布信息摘要","reason":"命中关注项"}`, Usage: provider.Usage{TotalTokens: 17}}, nil
	})
	worker := ArticleWorker{Service: service, Enabled: true}
	for _, job := range []*river.Job[ArticleArgs]{articleJob(f.users[0], a, 1, 1), articleJob(f.users[0], a, 1, 1), articleJob(f.users[1], a, 1, 1), articleJob(f.users[1], b, 1, 1), articleJob(f.users[0], unrelated, 1, 1), articleJob(f.users[0], stale, 1, 1)} {
		if err := worker.Work(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(calls, ",") != "model-0,model-1" {
		t.Fatalf("model calls = %v", calls)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_article_results WHERE user_id=ANY($1)`, f.users[:]).Scan(&count); err != nil || count != 2 {
		t.Fatalf("private result count = %d: %v", count, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_article_results WHERE user_id=$1 AND article_id=$2`, f.users[1], a).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cross-user result = %d: %v", count, err)
	}
	newer := f.article(t, "changed", "OpenAI next release", time.Now().Add(-30*time.Minute))
	if _, err := f.pool.Exec(ctx, `UPDATE user_model_configs SET config_version=2,model='changed-model',updated_at=now() WHERE user_id=$1`, f.users[0]); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, articleJob(f.users[0], newer, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, articleJob(f.users[0], newer, 2, 1)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatal("changing the model reprocessed an older queued article")
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_model_call_receipts WHERE user_id=ANY($1) AND status='completed' AND usage->>'total_tokens'='17'`, f.users[:]).Scan(&count); err != nil || count != 2 {
		t.Fatalf("usage receipt count = %d: %v", count, err)
	}
}

func TestPersonalArticleWorkerReadsSavedInterests(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	keywords := []string{"具身智能", `引号"`, `反\斜杠`}
	if err := personal.NewRepository(f.orm).SaveInterests(ctx, f.users[0], keywords, []string{"openai"}); err != nil {
		t.Fatal(err)
	}
	var created time.Time
	if err := f.pool.QueryRow(ctx, `SELECT now()`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	id := f.article(t, "saved_interests", "OpenAI 具身智能发布", created)
	calls := 0
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, func(_ context.Context, _ provider.Config, prompt string) (provider.Completion, error) {
		calls++
		if !strings.Contains(prompt, "具身智能") || !strings.Contains(prompt, "OpenAI") {
			t.Fatal("saved interests were not used in the model prompt")
		}
		return provider.Completion{Text: `{"relevanceScore":91,"title":"个人热点","summary":"具身智能发布信息摘要","reason":"命中关注项"}`}, nil
	})
	worker := ArticleWorker{Service: service, Enabled: true}
	if err := worker.Work(ctx, articleJob(f.users[0], id, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("an old preference version invoked the model")
	}
	if err := worker.Work(ctx, articleJob(f.users[0], id, 1, 2)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_article_results WHERE user_id=$1 AND article_id=$2`, f.users[0], id).Scan(&count); err != nil || count != 1 || calls != 1 {
		t.Fatalf("saved-interest result count=%d calls=%d err=%v", count, calls, err)
	}
}

func TestDailyWorkerUsesJobDateAndKeepsUsersSeparate(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	a := f.article(t, "dailyin", "OpenAI inside the fixed window", time.Now().Add(-time.Hour))
	b := f.article(t, "dailyout", "OpenAI after the fixed window", time.Now().Add(-time.Hour))
	c := f.article(t, "dailyother", "Anthropic user two result", time.Now().Add(-time.Hour))
	for _, item := range []struct {
		user            int64
		id, title, date string
	}{{f.users[0], a, "window-entry", "2026-10-03T12:00:00Z"}, {f.users[0], b, "after-window", "2026-10-04T01:00:00Z"}, {f.users[1], c, "other-user", "2026-10-03T12:00:00Z"}} {
		_, err := f.pool.Exec(ctx, `INSERT INTO user_article_results(user_id,article_id,input_revision,model,relevance_score,title,summary,created_at) VALUES($1,$2,1,'test',91,$3,'摘要',$4::timestamptz)`, item.user, item.id, item.title, item.date)
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, func(_ context.Context, cfg provider.Config, prompt string) (provider.Completion, error) {
		calls++
		if cfg.Model != "model-0" || !strings.Contains(prompt, "window-entry") || strings.Contains(prompt, "after-window") || strings.Contains(prompt, "other-user") {
			t.Fatalf("wrong daily input: %s", prompt)
		}
		return provider.Completion{Text: `{"title":"测试日报","summary":"只汇总固定窗口的本人热点"}`, Usage: provider.Usage{TotalTokens: 23}}, nil
	})
	worker := DailyWorker{Service: service, Enabled: true}
	job := &river.Job[DailyArgs]{JobRow: &rivertype.JobRow{ID: time.Now().UnixNano(), Attempt: 1}, Args: DailyArgs{UserID: f.users[0], Date: "2026-10-04"}}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("daily paid calls = %d", calls)
	}
	var body []byte
	if err := f.pool.QueryRow(ctx, `SELECT entries FROM user_daily_reports WHERE user_id=$1 AND report_date='2026-10-04'`, f.users[0]).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var entries []dailyEntry
	if err := json.Unmarshal(body, &entries); err != nil || len(entries) != 1 || entries[0].ArticleID != a {
		t.Fatalf("daily entries = %s: %v", body, err)
	}
}

func TestConnectionWorkerUsesEncryptedTaskAndReceiptOwner(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	var receipt int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO user_model_call_receipts(user_id,logical_key,purpose,model,status,request_hash) VALUES($1,$2,'connection_test','model-0','pending','testhash') RETURNING id`, f.users[0], f.prefix+"_connection").Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, func(_ context.Context, cfg provider.Config, _ string) (provider.Completion, error) {
		calls++
		if cfg.APIKey != "private-test-key-0" {
			t.Fatal("wrong decrypted credential")
		}
		return provider.Completion{Text: `{"ok":true}`, Usage: provider.Usage{TotalTokens: 5}}, nil
	})
	worker := ConnectionTestWorker{Service: service}
	args := ConnectionTestArgs{UserID: f.users[1], ReceiptID: receipt, BaseURL: "https://model.example.invalid/v1", Model: "model-0", APIKeyCiphertext: f.ciphertext[0]}
	job := &river.Job[ConnectionTestArgs]{JobRow: &rivertype.JobRow{ID: time.Now().UnixNano(), Attempt: 1}, Args: args}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("cross-user receipt invoked a model")
	}
	job.Args.UserID = f.users[0]
	encoded, _ := json.Marshal(job.Args)
	if strings.Contains(string(encoded), "private-test-key") {
		t.Fatal("task contains plaintext model key")
	}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("connection calls = %d", calls)
	}
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM user_model_call_receipts WHERE id=$1`, receipt).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("receipt status = %q: %v", status, err)
	}
}

func TestPersonalScanEnqueuesOnlyNewMatchingArticlesOnce(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	client, err := NewClient(ctx, f.pool, f.orm, f.cfg, nil, interests.PublicationSettings{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var activation time.Time
	if err := f.pool.QueryRow(ctx, `SELECT activated_at FROM personalization_rollout WHERE id=true`).Scan(&activation); err != nil {
		t.Fatal(err)
	}
	fresh := f.article(t, "scan_new", "OpenAI scanned news", activation.Add(time.Second))
	f.article(t, "scan_old", "OpenAI historical archive", activation.Add(-time.Second))
	stale := f.article(t, "scan_stale", "OpenAI stale first import", activation.Add(time.Second))
	if _, err := f.pool.Exec(ctx, `UPDATE articles SET published_at=discovered_at-interval '49 hours' WHERE id=$1`, stale); err != nil {
		t.Fatal(err)
	}
	f.article(t, "scan_unrelated", "Weather scanned news", activation.Add(time.Second))
	worker := ScanWorker{Service: NewService(NewRepository(f.orm, f.pool), f.cfg, nil), Client: client, Enabled: true}
	if err := worker.Work(ctx, &river.Job[ScanArgs]{}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, &river.Job[ScanArgs]{}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='myhot_personal_article' AND args->>'articleId' LIKE $1`, f.prefix+"_%").Scan(&count); err != nil || count != 1 {
		t.Fatalf("queued personal jobs = %d: %v", count, err)
	}
	var encoded []byte
	if err := f.pool.QueryRow(ctx, `SELECT args FROM river_job WHERE kind='myhot_personal_article' AND args->>'articleId'=$1`, fresh).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var args ArticleArgs
	if err := json.Unmarshal(encoded, &args); err != nil {
		t.Fatal(err)
	}
	if args.UserID != f.users[0] || args.ModelVersion != 1 || args.PreferenceVersion != 1 {
		t.Fatalf("queued args = %+v", args)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'articleId' LIKE $1`, f.prefix+"_%")
	})
}

/** TestSourceAutomaticallyFollowsStableLabels verifies automatic and manual follows through the same scan. */
func TestSourceAutomaticallyFollowsStableLabels(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	id := f.prefix + "-label"
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, "DELETE FROM interest_labels WHERE id=$1", id) })
	if _, err := f.pool.Exec(ctx, "UPDATE user_interests SET keywords='{}',label_ids=ARRAY[$1::text] WHERE user_id=$2", id, f.users[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE user_interests SET keywords='{}' WHERE user_id=$1", f.users[1]); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, nil)
	service.Labels = tagging.NewService(tagging.NewRepository(f.orm), tagging.Builtins(nil, []topics.Definition{
		{Slug: id, Name: "集成测试技术", Group: "field", Aliases: []string{"技术验证"}, Definition: "检查技术主题在真实队列扫描中的匹配行为"},
	}), nil)
	if err := service.repository.EnsureRollout(ctx); err != nil {
		t.Fatal(err)
	}
	match := f.article(t, "tagged", "技术验证发布", time.Now())
	f.article(t, "untagged", "天气预报", time.Now())
	unclassified := f.prefix + "_unclassified"
	firstArticle := f.prefix + "_first"
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, "DELETE FROM articles WHERE id=$1", firstArticle)
		_, _ = f.pool.Exec(ctx, "DELETE FROM sources WHERE id=$1", unclassified)
	})
	if _, err := f.pool.Exec(ctx, "INSERT INTO sources(id,name,kind) VALUES($1,'Unclassified','rss')", unclassified); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "INSERT INTO user_source_subscriptions(user_id,source_id,name,activated_at) VALUES($1,$2,'Unclassified',now()-interval '1 minute')", f.users[1], unclassified); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,created_at) VALUES($1,$2,$1,$3,'海洋观测报告',now(),now(),now())", firstArticle, unclassified, "https://articles.example.invalid/"+firstArticle); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "INSERT INTO article_discoveries(article_id,source_id,via) VALUES($1,$2,'fetch')", firstArticle, unclassified); err != nil {
		t.Fatal(err)
	}
	var queued []ArticleArgs
	if err := service.Scan(ctx, func(_ context.Context, _ pgx.Tx, args ArticleArgs) error {
		if args.UserID == f.users[0] || args.UserID == f.users[1] {
			queued = append(queued, args)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 3 {
		t.Fatalf("expected two tag matches and one article from the unclassified source: %+v", queued)
	}
	for _, args := range queued {
		input, err := service.repository.ArticleInput(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		if args.ArticleID == firstArticle && args.UserID == f.users[1] && args.SourceID == unclassified {
			if len(input.SourceLabelIDs) != 0 {
				t.Fatalf("unclassified source inherited another source's labels: %v", input.SourceLabelIDs)
			}
			continue
		}
		if args.ArticleID != match || args.SourceID != f.source {
			t.Fatalf("wrong queued article or subscription: %+v", args)
		}
		if len(input.SourceLabelIDs) != 1 || input.SourceLabelIDs[0] != id {
			t.Fatalf("worker did not read the source's automatic follow: %v", input.SourceLabelIDs)
		}
	}
	var linked bool
	if err := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM article_interest_labels WHERE article_id=$1 AND label_id=$2)", match, id).Scan(&linked); err != nil || !linked {
		t.Fatalf("article was not indexed before dispatch: %v", err)
	}
}

func TestPersonalScanRevisitsArticleWhenAnotherSubscribedSourceDiscoversItLater(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	secondSource := f.prefix + "_source_second"
	if _, err := f.pool.Exec(ctx, `INSERT INTO sources(id,name,kind,config) VALUES($1,'Second worker source','rss','{"feedUrl":"https://worker.example.invalid/second"}')`, secondSource); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM user_source_subscriptions WHERE source_id=$1`, secondSource)
		_, _ = f.pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, secondSource)
	})
	if _, err := f.pool.Exec(ctx, `UPDATE user_source_subscriptions SET enabled=false WHERE user_id=$1 AND source_id=$2`, f.users[0], f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO user_source_subscriptions(user_id,source_id,name,activated_at) VALUES($1,$2,'Second source',now()-interval '1 minute')`, f.users[0], secondSource); err != nil {
		t.Fatal(err)
	}
	articleID := f.article(t, "late_second_source", "OpenAI publishes an update", time.Now())
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, nil)
	var queued []ArticleArgs
	enqueue := func(_ context.Context, _ pgx.Tx, args ArticleArgs) error {
		queued = append(queued, args)
		return nil
	}
	if err := service.Scan(ctx, enqueue); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("first discovery unexpectedly reached a user without an active subscription: %#v", queued)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO article_discoveries(article_id,source_id,via,discovered_at) VALUES($1,$2,'fetch',now())`, articleID, secondSource); err != nil {
		t.Fatal(err)
	}
	if err := service.Scan(ctx, enqueue); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].UserID != f.users[0] || queued[0].ArticleID != articleID {
		t.Fatalf("late second-source discovery was not dispatched to its subscriber: %#v", queued)
	}
}

func TestRiverConsumesPersonalArticleWithFakeModel(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	articleID := f.article(t, "queued", "OpenAI queued release", time.Now().Add(-time.Hour))
	var calls atomic.Int32
	workers := river.NewWorkers()
	service := NewService(NewRepository(f.orm, f.pool), f.cfg, func(_ context.Context, cfg provider.Config, _ string) (provider.Completion, error) {
		calls.Add(1)
		return provider.Completion{Text: `{"relevanceScore":92,"title":"队列个人热点","summary":"真实River队列消费，模型仅使用fake"}`, Usage: provider.Usage{TotalTokens: 31}}, nil
	})
	river.AddWorker(workers, &ArticleWorker{Service: service, Enabled: true})
	queue := f.prefix + "_queue"
	client, err := river.NewClient(riverpgxv5.New(f.pool), &river.Config{Workers: workers, Queues: map[string]river.QueueConfig{queue: {MaxWorkers: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	}()
	job, err := client.Insert(ctx, ArticleArgs{UserID: f.users[0], ArticleID: articleID, Revision: 1, ModelVersion: 1, PreferenceVersion: 1}, &river.InsertOpts{Queue: queue, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var state string
		if err := f.pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id=$1`, job.Job.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			break
		}
		if state == "discarded" || time.Now().After(deadline) {
			t.Fatalf("queue state = %s", state)
		}
		time.Sleep(30 * time.Millisecond)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_article_results WHERE user_id=$1 AND article_id=$2`, f.users[0], articleID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("consumed result count = %d: %v", count, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM user_model_call_receipts WHERE user_id=$1 AND article_id=$2 AND status='completed' AND usage->>'total_tokens'='31'`, f.users[0], articleID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("consumed receipt count = %d: %v", count, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("queued paid calls = %d", calls.Load())
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM river_job WHERE queue=$1`, queue) })
}

func TestSourceJobsAllowNextRunAfterCompletion(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.cfg.CollectorEnabled = true
	f.cfg.SourceSchedule = config.SourceSchedule{
		AutoMinInterval:   15,
		ManualReuseWindow: 10,
		AutoReuseWindow:   10,
	}
	if _, err := f.pool.Exec(ctx, `UPDATE user_source_subscriptions SET interval_minutes=5 WHERE source_id=$1`, f.source); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ctx, f.pool, f.orm, f.cfg, nil, interests.PublicationSettings{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var interval int
	if err := f.pool.QueryRow(ctx, `SELECT interval_minutes FROM sources WHERE id=$1`, f.source).Scan(&interval); err != nil || interval != 15 {
		t.Fatalf("worker did not reconcile schedule before scanning: interval=%d err=%v", interval, err)
	}
	args := SourceArgs{SourceID: f.source}
	first, err := client.Insert(ctx, args, sourceInsertOpts())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Insert(ctx, args, sourceInsertOpts())
	if err != nil || second.Job.ID != first.Job.ID {
		t.Fatalf("active source jobs are not unique: %v", err)
	}
	manual, err := client.Insert(ctx, SourceArgs{SourceID: f.source, Force: true}, sourceInsertOpts())
	if err != nil || manual.Job.ID != first.Job.ID {
		t.Fatalf("manual and automatic source jobs were not coalesced: %v", err)
	}
	manualAgain, err := client.Insert(ctx, SourceArgs{SourceID: f.source, Force: true}, sourceInsertOpts())
	if err != nil || manualAgain.Job.ID != first.Job.ID {
		t.Fatalf("duplicate manual fetch was not deduplicated: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE river_job SET state='completed',finalized_at=now() WHERE id=$1`, first.Job.ID); err != nil {
		t.Fatal(err)
	}
	next, err := client.Insert(ctx, args, sourceInsertOpts())
	if err != nil || next.Job.ID == first.Job.ID {
		t.Fatalf("completed job prevents next collection: %v", err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'sourceId'=$1`, f.source) })
}

func TestRiverSchemaStartupSerializesConcurrentAPIAndWorkerMigrations(t *testing.T) {
	pool := localTestPool(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- MigrateRiver(context.Background(), pool)
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublicationEffectsInsertRiverJobsInPublicationTransaction(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	cfg := f.cfg
	cfg.ModelCallsEnabled, cfg.SharedPipelineEnabled = false, false
	cfg.GoContentDeliveryEnabled, cfg.GoMediaPrepareEnabled = false, false
	cfg.WebOGPrewarmURL, cfg.WebOGPrewarmSecret = "http://web.invalid/prewarm", strings.Repeat("x", 32)
	client, err := NewClient(ctx, f.pool, f.orm, cfg, nil, interests.PublicationSettings{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	articleID := f.prefix + "_effects"
	effects := &publicationRiverEffects{client: client}
	count := func() int {
		var value int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'articleId'=$1 AND kind IN ('notify.selected','media.prepare')`, articleID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := effects.EnqueueSelectedTx(ctx, tx, articleID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := effects.EnqueueMediaTx(ctx, tx, articleID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 0 {
		t.Fatalf("River side effects escaped a rolled-back publication: %d", got)
	}
	tx, err = f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := effects.EnqueueSelectedTx(ctx, tx, articleID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := effects.EnqueueMediaTx(ctx, tx, articleID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 2 {
		t.Fatalf("committed publication effects = %d, want 2", got)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'articleId'=$1`, articleID) })
}

/** TestFailedDatabaseOpenDoesNotPoisonLaterRiverQueueInsertion covers a fresh pool after a failed database open. */
func TestFailedDatabaseOpenDoesNotPoisonLaterRiverQueueInsertion(t *testing.T) {
	ctx := context.Background()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("integration database must be localhost and end with _test")
	}

	missing := *parsed
	missing.Path = "/myhot_missing_queue_recovery_test"
	if pool, err := db.Open(ctx, missing.String()); err == nil {
		pool.Close()
		t.Fatal("expected opening a missing test database to fail")
	}

	pool, err := db.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := MigrateRiver(ctx, pool); err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.Insert(ctx, SourceArgs{SourceID: "queue-recovery-test"}, &river.InsertOpts{Queue: "queue_recovery_test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM river_job WHERE id=$1`, job.Job.ID)
	})
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE id=$1 AND kind=$2`, job.Job.ID, SourceArgs{}.Kind()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("queue job count after recovery = %d, want 1", count)
	}
}

/** TestVerifiedPublisherAttributionQueuesBodyExtractionOnce keeps discovery and body work distinct. */
func TestVerifiedPublisherAttributionQueuesBodyExtractionOnce(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	if err := MigrateRiver(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	discoveryID := f.prefix + "_verified_discovery"
	publisherID := f.prefix + "_verified_publisher"
	articleURL := "https://publisher-" + f.prefix + ".example/news/release"
	configBytes, err := json.Marshal(map[string]any{
		"publisherUrlPrefixes": []string{"https://publisher-" + f.prefix + ".example/news"},
		"fetchPublicContent":   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO sources(id,name,kind,tier,participation_mode,config,enabled,health)
		VALUES($1,'Verified discovery','rss','T1','editorial','{}'::jsonb,true,'ok'),
		($2,'Paused verified publisher','web_list','T1','editorial',$3::jsonb,false,'paused')`, discoveryID, publisherID, configBytes); err != nil {
		t.Fatal(err)
	}
	publicationRepository := publicationdomain.NewRepository(f.pool, nil, interests.PublicationSettings{}, "Test", "https://test.invalid")
	materialService := materials.NewService(materials.NewRepository(f.orm))
	materialService.SetSourceAttributor(materials.NewAttributionRepository(f.pool, publicationRepository))
	stored, err := materialService.Store(ctx, materials.Input{
		SourceID: discoveryID, URL: articleURL, IdentityKey: "url:" + articleURL,
		Title: "Publisher announcement", Author: "Aggregator submitter", Via: "ingest",
	})
	if err != nil || !stored.Created || stored.Revised {
		t.Fatalf("initial discovery = %#v, err=%v", stored, err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM river_job WHERE args->>'articleId'=$1`, stored.ArticleID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM audit_log WHERE action='article.attribution' AND subject=$1`, "article:"+stored.ArticleID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM articles WHERE id=$1`, stored.ArticleID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM sources WHERE id=ANY($1)`, []string{discoveryID, publisherID})
	})
	if _, err := f.pool.Exec(ctx, `UPDATE articles SET created_at=now()-interval '5 minutes' WHERE id=$1`, stored.ArticleID); err != nil {
		t.Fatal(err)
	}
	var correctedSource string
	var author *string
	if err := f.pool.QueryRow(ctx, `SELECT source_id,author FROM articles WHERE id=$1`, stored.ArticleID).Scan(&correctedSource, &author); err != nil {
		t.Fatal(err)
	}
	if correctedSource != publisherID || author != nil {
		t.Fatalf("verified publisher attribution source=%q author=%v", correctedSource, author)
	}
	service := articlebody.NewService(articlebody.NewRepository(f.orm, true, true), nil, nil)
	client, err := river.NewClient(riverpgxv5.New(f.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	scan := &SharedBodyScanWorker{Service: service, Client: client, Enabled: true}
	for range 2 {
		if err := scan.Work(ctx, &river.Job[SharedBodyScanArgs]{}); err != nil {
			t.Fatal(err)
		}
	}
	var jobs, discoveries int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND args->>'articleId'=$2`, SharedBodyArgs{}.Kind(), stored.ArticleID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM article_discoveries WHERE article_id=$1 AND source_id=$2`, stored.ArticleID, discoveryID).Scan(&discoveries); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || discoveries != 1 {
		t.Fatalf("body tasks=%d discovery records=%d, want one each", jobs, discoveries)
	}
}

/** TestRiverStopDrainsAnInflightPaidAnalysis verifies graceful shutdown waits for durable work. */
func TestRiverStopDrainsAnInflightPaidAnalysis(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	if err := MigrateRiver(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	articleID := f.article(t, "shutdown", "OpenAI shutdown release", time.Now().Add(-time.Hour))
	if _, err := f.pool.Exec(ctx, `UPDATE sources SET tier='T1',participation_mode='editorial' WHERE id=$1`, f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE articles SET body_text='A lab released a new AI model with evaluation results.',body_status='ok' WHERE id=$1`, articleID); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	openRelease := func() {
		releaseOnce.Do(func() { close(release) })
	}
	defer openRelease()
	analyzer := &shutdownAnalysisStub{pool: f.pool, entered: entered, release: release}
	publicationRepository := publicationdomain.NewRepository(f.pool, nil, interests.PublicationSettings{}, "Test", "https://test.invalid")
	service := contentpipeline.NewService(contentpipeline.NewRepository(f.orm), analyzer, nil,
		publicationdomain.NewService(publicationRepository), nil)
	workers := river.NewWorkers()
	worker := &ContentAnalyzeWorker{Service: service}
	river.AddWorker(workers, worker)
	river.AddWorker(workers, &EventGroupWorker{})
	queue := "shared_analysis"
	queues := map[string]river.QueueConfig{queue: {MaxWorkers: 1}}
	client, err := river.NewClient(riverpgxv5.New(f.pool), &river.Config{Workers: workers, Queues: queues})
	if err != nil {
		t.Fatal(err)
	}
	worker.Client = client
	var stoppedCleanly atomic.Bool
	t.Cleanup(func() {
		openRelease()
		if !stoppedCleanly.Load() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = client.Stop(stopCtx)
		}
	})
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := client.Insert(ctx, ContentAnalyzeArgs{ArticleID: articleID, AttemptTag: "shutdown-test"}, &river.InsertOpts{Queue: queue, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM river_job WHERE id=$1 OR args->>'articleId'=$2`, job.Job.ID, articleID)
	})
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("paid analysis did not reach the fake model")
	}

	stopped := make(chan error, 1)
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopped <- client.Stop(stopCtx)
	}()
	select {
	case err := <-stopped:
		openRelease()
		t.Fatalf("River stopped before the in-flight model call settled: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	openRelease()
	if err := <-stopped; err != nil {
		stoppedCleanly.Store(true)
		t.Fatal(err)
	}
	stoppedCleanly.Store(true)
	var state string
	var jobErrors []byte
	var attempts int
	if err := f.pool.QueryRow(ctx, `SELECT state,errors::text,attempt FROM river_job WHERE id=$1`, job.Job.ID).Scan(&state, &jobErrors, &attempts); err != nil || state != "completed" {
		t.Fatalf("stopped worker job state = %q, attempts=%d errors=%s, err=%v", state, attempts, jobErrors, err)
	}
	var analyses, publications int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM analyses WHERE article_id=$1 AND input_revision=1 AND relevance='pass'`, articleID).Scan(&analyses); err != nil || analyses != 1 {
		t.Fatalf("saved shared analysis = %d, err=%v", analyses, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM publications WHERE article_id=$1 AND selection_candidate`, articleID).Scan(&publications); err != nil || publications != 1 {
		t.Fatalf("saved publication candidate = %d, err=%v", publications, err)
	}
	if analyzer.calls.Load() != 1 {
		t.Fatalf("fake paid analysis calls during graceful stop = %d, want 1", analyzer.calls.Load())
	}
}

type shutdownAnalysisStub struct {
	pool    *pgxpool.Pool
	entered chan<- struct{}
	release <-chan struct{}
	calls   atomic.Int32
}

/** Analyze blocks like an in-flight paid writing call, then commits its result for publication. */
func (s *shutdownAnalysisStub) Analyze(ctx context.Context, articleID string, _ editorial.AnalysisOptions) (*editorial.AnalyzeResult, error) {
	s.calls.Add(1)
	s.entered <- struct{}{}
	<-s.release
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var analysisID int64
	err = tx.QueryRow(ctx, `INSERT INTO analyses(article_id,input_revision,origin,prompt_version,relevance,category,tags,subjects,title_zh,summary_zh,score,selected,output)
		VALUES($1,1,'model','shutdown-test','pass','models',ARRAY['模型发布']::text[],ARRAY[]::text[],'停机期间完成','模型分析结果已写入并发布。',92,true,'{"scope":"single"}'::jsonb) RETURNING id`, articleID).Scan(&analysisID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE articles SET processing_state='analyzed' WHERE id=$1 AND revision=1`, articleID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	score := 92
	category := "models"
	return &editorial.AnalyzeResult{AnalysisID: &analysisID, Output: &editorial.AnalysisOutput{
		Relevance: "pass", Selected: true, Score: &score, Category: &category, Tags: []string{"模型发布"}, Subjects: []string{},
		TitleZh: "停机期间完成", SummaryZh: "模型分析结果已写入并发布。", Scope: "single",
	}, ReceiptIDs: []int64{}, Reused: false}, nil
}
