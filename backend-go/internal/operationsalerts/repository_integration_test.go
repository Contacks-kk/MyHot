package operationsalerts

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/sourcehealth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIntegrationRepository(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !(strings.HasSuffix(parsed.Path, "_test") || strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("integration database must be localhost and end in _test or _ci")
	}
	orm, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := orm.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return orm
}

func TestRepositoryFindingsReadMigratedTestSchema(t *testing.T) {
	orm := openIntegrationRepository(t)
	findings, err := NewRepository(orm).Findings(context.Background(), time.Now().UTC(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Key == "" || finding.Title == "" || (finding.Level != LevelNow && finding.Level != LevelToday) {
			t.Fatalf("invalid finding from migrated database: %+v", finding)
		}
	}
}

/** TestRepositorySourceHealthFindingsPreserveRecentEvidence verifies the migrated source evidence query. */
func TestRepositorySourceHealthFindingsPreserveRecentEvidence(t *testing.T) {
	orm := openIntegrationRepository(t).Begin()
	if orm.Error != nil {
		t.Fatal(orm.Error)
	}
	t.Cleanup(func() { _ = orm.Rollback().Error })
	now := time.Date(2026, time.October, 8, 1, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	prefix := fmt.Sprintf("source_health_%d", time.Now().UnixNano())
	insertSource := func(id, mode, health string, createdAt time.Time) {
		if err := orm.Exec(`INSERT INTO sources(id,name,kind,participation_mode,health,fail_count,last_error,created_at)
			VALUES(?, ?, 'rss', ?, ?, 6, 'HTTP 503', ?)`, id, id, mode, health, createdAt).Error; err != nil {
			t.Fatal(err)
		}
	}
	stoppedID := prefix + "_stopped"
	qualityID := prefix + "_quality"
	signalID := prefix + "_signal"
	budgetID := prefix + "_budget"
	insertSource(stoppedID, "editorial", "failing", cutoff.Add(-time.Hour))
	insertSource(qualityID, "editorial", "ok", cutoff.Add(time.Hour))
	insertSource(signalID, "hot_signal", "ok", cutoff.Add(time.Hour))
	insertSource(budgetID, "editorial", "ok", cutoff.Add(time.Hour))
	for _, sourceID := range []string{qualityID, signalID} {
		if err := orm.Exec(`INSERT INTO fetch_runs(source_id,started_at,status,error,detail)
			SELECT ?, ? - i * interval '1 hour', CASE WHEN i <= 5 THEN 'failed' ELSE 'ok' END,
			CASE WHEN i <= 5 THEN 'HTTP 503' ELSE NULL END,
			jsonb_build_object('detailFailures', CASE WHEN i = 1 THEN 2 ELSE 0 END)
			FROM generate_series(1,20) AS i`, sourceID, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := orm.Exec(`INSERT INTO fetch_runs(source_id,started_at,status,error)
		SELECT ?, ? - i * interval '1 hour', 'failed', 'Budget for test exhausted (day)' FROM generate_series(1,20) AS i`, budgetID, now).Error; err != nil {
		t.Fatal(err)
	}
	articleID := prefix + "_undated"
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,published_at,discovered_at,timeline_at)
		VALUES(?,?,?,?,'Undated article',NULL,?,?)`, articleID, qualityID, "url:"+articleID, "https://source-health.invalid/"+articleID, now.Add(-time.Hour), now.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,published_at,discovered_at,timeline_at)
		VALUES(?,?,?,?,'Signal article',NULL,?,?)`, prefix+"_signal_article", signalID, "url:"+prefix+"_signal_article", "https://source-health.invalid/signal", now.Add(-time.Hour), now.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO article_revisions(article_id,revision,title,created_at)
		SELECT ?, i, 'Updated article', ? FROM generate_series(2,6) AS i`, articleID, now.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := sourcehealth.NewRepository(orm).Weekly(context.Background(), cutoff, cutoff.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	findings := sourceHealthFindings(snapshot.Sources, cutoff)
	byKey := make(map[string]Finding, len(findings))
	for _, finding := range findings {
		byKey[finding.Key] = finding
	}
	for _, key := range []string{"sources.failing.editorial", "sources.silent.editorial", "sources.quality.editorial", "sources.unstable.editorial", "sources.unstable.hot_signal", "sources.details.editorial", "sources.details.hot_signal"} {
		if _, exists := byKey[key]; !exists {
			t.Errorf("missing repository-backed source-health finding %q", key)
		}
	}
	if _, exists := byKey["sources.quality.hot_signal"]; exists {
		t.Fatal("hot-signal articles were classified as editorial quality failures")
	}
	if strings.Contains(byKey["sources.unstable.editorial"].Detail, budgetID) {
		t.Fatalf("budget-exhausted runs were counted as unstable: %s", byKey["sources.unstable.editorial"].Detail)
	}
	if !strings.Contains(byKey["sources.quality.editorial"].Detail, qualityID) ||
		!strings.Contains(byKey["sources.silent.editorial"].Detail, stoppedID) {
		t.Fatalf("repository evidence was assigned to the wrong sources: %#v", byKey)
	}
}

func TestRepositoryAlertStateRoundTripWithinTransaction(t *testing.T) {
	orm := openIntegrationRepository(t).Begin()
	if orm.Error != nil {
		t.Fatal(orm.Error)
	}
	t.Cleanup(func() { _ = orm.Rollback().Error })
	repository := NewRepository(orm)
	want := State{
		"integration.roundtrip": {
			Title:  "integration alert",
			Since:  time.Date(2026, time.October, 8, 1, 2, 3, 0, time.UTC),
			SentAt: time.Date(2026, time.October, 8, 1, 12, 3, 0, time.UTC),
		},
	}
	if err := repository.SaveState(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := repository.LoadState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["integration.roundtrip"] != want["integration.roundtrip"] {
		t.Fatalf("alert state round trip = %#v, want %#v", got, want)
	}
}

/** TestRepositoryWaitingGroupingPreservesReaderImpactBoundaries verifies alert filters and receipt recovery states. */
func TestRepositoryWaitingGroupingPreservesReaderImpactBoundaries(t *testing.T) {
	orm := openIntegrationRepository(t).Begin()
	if orm.Error != nil {
		t.Fatal(orm.Error)
	}
	t.Cleanup(func() { _ = orm.Rollback().Error })
	var pgBossExists bool
	if err := orm.Raw("SELECT to_regclass('pgboss.job') IS NOT NULL").Scan(&pgBossExists).Error; err != nil {
		t.Fatal(err)
	}
	if !pgBossExists {
		t.Skip("legacy pg-boss schema is not installed in the local test database")
	}

	now := time.Now().UTC().Truncate(time.Second)
	prefix := fmt.Sprintf("group_alert_%d", now.UnixNano())
	if err := orm.Exec(`INSERT INTO sources(id,name,kind,tier,participation_mode)
		VALUES(?, 'Grouping alert integration', 'rss', 'T1', 'editorial')`, prefix).Error; err != nil {
		t.Fatal(err)
	}
	makeArticle := func(id string, age time.Duration, groupingStatus string, candidate bool, visibility string) {
		t.Helper()
		at := now.Add(-age)
		if err := orm.Exec(`INSERT INTO articles(id,source_id,identity_key,url,title,published_at,discovered_at,timeline_at,grouping_status,grouping_error)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, id, prefix, id, "https://grouping-alert.invalid/"+id, id, at, at, at, groupingStatus, "fixture error").Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`INSERT INTO publications(article_id,source_id,title,summary,eligible,selected,selection_candidate,visibility,
			selected_ready_at,discovered_at,timeline_at,sort_at,url,category,channel)
			VALUES(?,?,?,'summary',true,false,?,?, ?,?,?,?,?,'industry','news')`, id, prefix, id, candidate, visibility, at, at, at, at, "https://grouping-alert.invalid/"+id).Error; err != nil {
			t.Fatal(err)
		}
	}

	makeArticle(prefix+"_short_wait", 2*time.Minute, "pending", true, "public")
	makeArticle(prefix+"_new_revision", 48*time.Hour, "pending", true, "public")
	if err := orm.Exec(`INSERT INTO article_revisions(article_id,revision,title,created_at) VALUES(?,1,'new revision',?)`, prefix+"_new_revision", now).Error; err != nil {
		t.Fatal(err)
	}
	makeArticle(prefix+"_complete", 30*time.Minute, "complete", true, "public")
	makeArticle(prefix+"_rejected", 30*time.Minute, "failed", false, "public")
	makeArticle(prefix+"_withdrawn", 30*time.Minute, "failed", true, "withdrawn")
	makeArticle(prefix+"_manual", 20*time.Minute, "failed", true, "public")
	makeArticle(prefix+"_unknown", 30*time.Minute, "failed", true, "public")
	makeArticle(prefix+"_released", 30*time.Minute, "failed", true, "public")

	insertUnknownReceipt := func(suffix string) int64 {
		t.Helper()
		var receiptID int64
		err := orm.Raw(`INSERT INTO receipts(logical_key,service,purpose,subject,status)
			VALUES(?, 'deepseek', 'event_identity', ?, 'unknown') RETURNING id`, prefix+"_"+suffix, "article:"+prefix+"_"+suffix).Row().Scan(&receiptID)
		if err != nil {
			t.Fatal(err)
		}
		if err := orm.Exec(`UPDATE articles SET grouping_receipt_id=? WHERE id=?`, receiptID, prefix+"_"+suffix).Error; err != nil {
			t.Fatal(err)
		}
		return receiptID
	}
	insertUnknownReceipt("unknown")
	releasedReceiptID := insertUnknownReceipt("released")
	if err := orm.Exec(`INSERT INTO receipt_attempts(receipt_id,attempt,service,status,error)
		VALUES(?,1,'deepseek','failed',?)`, releasedReceiptID, autoReleaseNote+"; fixture").Error; err != nil {
		t.Fatal(err)
	}

	items, err := NewRepository(orm).waitingGrouping(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	byArticle := make(map[string]groupingItem, len(items))
	for _, item := range items {
		byArticle[item.ArticleID] = item
	}
	wantRecovery := map[string]string{
		prefix + "_manual":   "manual",
		prefix + "_unknown":  "receipt",
		prefix + "_released": "manual",
	}
	if len(byArticle) != len(wantRecovery) {
		t.Fatalf("waiting grouping article set = %#v", byArticle)
	}
	for articleID, recovery := range wantRecovery {
		if item, ok := byArticle[articleID]; !ok || item.Recovery != recovery {
			t.Errorf("article %s recovery = %+v, want %q", articleID, item, recovery)
		}
	}
}

/** TestWorkerWatchdogRepositoryRoundTrip verifies the worker state store under its advisory lock. */
func TestWorkerWatchdogRepositoryRoundTrip(t *testing.T) {
	orm := openIntegrationRepository(t).Begin()
	if orm.Error != nil {
		t.Fatal(orm.Error)
	}
	t.Cleanup(func() { _ = orm.Rollback().Error })
	heartbeatAt := time.Date(2026, time.October, 8, 1, 2, 3, 0, time.UTC)
	if err := orm.Exec(`INSERT INTO settings (key, value, updated_by, updated_at)
		VALUES ('heartbeat.worker', '{}'::jsonb, 'integration', ?)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`, heartbeatAt).Error; err != nil {
		t.Fatal(err)
	}
	want := WorkerWatchdogState{
		State: "down",
		Since: heartbeatAt,
	}
	var gotHeartbeat *time.Time
	var gotState *WorkerWatchdogState
	err := NewRepository(orm).WithWorkerWatchdogLock(context.Background(), func(repository WorkerWatchdogStateRepository) error {
		var err error
		gotHeartbeat, err = repository.WorkerHeartbeatUpdatedAt(context.Background())
		if err != nil {
			return err
		}
		if err := repository.SaveWorkerWatchdogState(context.Background(), want); err != nil {
			return err
		}
		gotState, err = repository.LoadWorkerWatchdogState(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotHeartbeat == nil || !gotHeartbeat.Equal(heartbeatAt) {
		t.Fatalf("worker heartbeat = %v, want %s", gotHeartbeat, heartbeatAt)
	}
	if gotState == nil || gotState.State != want.State || !gotState.Since.Equal(want.Since) {
		t.Fatalf("worker watchdog state = %#v, want %#v", gotState, want)
	}
}
