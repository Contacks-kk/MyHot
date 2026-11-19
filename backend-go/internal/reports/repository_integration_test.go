package reports

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func reportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("MYHOT_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("MYHOT_TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || (!strings.HasSuffix(parsed.Path, "_test") && !strings.HasSuffix(parsed.Path, "_ci")) {
		t.Fatal("database must be localhost and end with _test or _ci")
	}
	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

/** TestPeriodReportsAssignsDelayedAndBoundaryReleasesOnce keeps items in the issue readers first see them. */
func TestPeriodReportsAssignsDelayedAndBoundaryReleasesOnce(t *testing.T) {
	db := reportTestDB(t)
	repository := NewRepository(db)
	ctx := context.Background()
	prefix := fmt.Sprintf("report_window_%d", time.Now().UnixNano())
	sourceID := prefix + "_source"
	start := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	items := []struct {
		id         string
		timelineAt time.Time
		visibleAt  time.Time
	}{
		{id: prefix + "_on_time", timelineAt: start.Add(time.Hour), visibleAt: start.Add(-time.Hour)},
		{id: prefix + "_delayed", timelineAt: start.Add(2 * time.Hour), visibleAt: end.Add(time.Hour)},
		{id: prefix + "_boundary", timelineAt: start.Add(3 * time.Hour), visibleAt: end},
	}
	t.Cleanup(func() {
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.id)
		}
		_ = db.Exec("DELETE FROM publications WHERE article_id IN ?", ids).Error
		_ = db.Exec("DELETE FROM articles WHERE id IN ?", ids).Error
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := db.Exec("INSERT INTO sources(id, name, kind) VALUES(?, 'Report window fixture', 'rss')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := db.Exec(`INSERT INTO articles(id, source_id, identity_key, url, title, discovered_at, timeline_at)
 VALUES(?, ?, ?, ?, ?, ?, ?)`, item.id, sourceID, item.id, "https://report.example/"+item.id, item.id, item.timelineAt, item.timelineAt).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO publications(article_id, source_id, title, url, channel, discovered_at, timeline_at,
 visibility, eligible, selected, backfill, visible_after)
 VALUES(?, ?, ?, ?, 'news', ?, ?, 'public', true, true, false, ?)`,
			item.id, sourceID, item.id, "https://report.example/"+item.id, item.timelineAt, item.timelineAt, item.visibleAt).Error; err != nil {
			t.Fatal(err)
		}
	}

	previous, err := repository.PeriodReports(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	next, err := repository.PeriodReports(ctx, end, end.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	previousIDs := make(map[string]bool, len(previous))
	issueCounts := make(map[string]int, len(items))
	for _, row := range previous {
		previousIDs[row.ID] = true
		issueCounts[row.ID]++
	}
	nextIDs := make(map[string]bool, len(next))
	for _, row := range next {
		nextIDs[row.ID] = true
		issueCounts[row.ID]++
	}
	if len(previousIDs) != 1 || !previousIDs[items[0].id] {
		t.Fatalf("previous issue candidates = %#v, want only the on-time release", previousIDs)
	}
	if len(nextIDs) != 2 || !nextIDs[items[1].id] || !nextIDs[items[2].id] {
		t.Fatalf("next issue candidates = %#v, want delayed and boundary releases", nextIDs)
	}
	for _, item := range items {
		if issueCounts[item.id] != 1 {
			t.Fatalf("item %s appeared in %d issue windows, want exactly one", item.id, issueCounts[item.id])
		}
	}
}

/** waitForReportLockWaits confirms PostgreSQL readers and publishers queued behind a report snapshot lock. */
func waitForReportLockWaits(t *testing.T, db *sql.DB, blockerPID, minimum int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, blockerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= minimum {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fewer than %d PostgreSQL operations waited for backend %d", minimum, blockerPID)
}

/** TestPeriodReportsWaitsForAnEarlierReleaseTransaction protects the issue cutoff from commit races. */
func TestPeriodReportsWaitsForAnEarlierReleaseTransaction(t *testing.T) {
	db := reportTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("report_lock_%d", time.Now().UnixNano())
	sourceID, articleID := prefix+"_source", prefix+"_article"
	start := time.Date(2099, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM publications WHERE article_id = ?", articleID).Error
		_ = db.Exec("DELETE FROM articles WHERE id = ?", articleID).Error
		_ = db.Exec("DELETE FROM sources WHERE id = ?", sourceID).Error
	})
	if err := db.Exec("INSERT INTO sources(id, name, kind) VALUES(?, 'Report lock fixture', 'rss')", sourceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO articles(id, source_id, identity_key, url, title, discovered_at, timeline_at)
 VALUES(?, ?, ?, ?, ?, ?, ?)`, articleID, sourceID, articleID, "https://report.example/"+articleID, articleID, start.Add(time.Hour), start.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO publications(article_id, source_id, title, url, channel, discovered_at, timeline_at,
 visibility, eligible, selected, backfill, visible_after)
 VALUES(?, ?, ?, ?, 'news', ?, ?, 'public', true, true, false, ?)`,
		articleID, sourceID, articleID, "https://report.example/"+articleID, start.Add(time.Hour), start.Add(time.Hour), start).Error; err != nil {
		t.Fatal(err)
	}

	blocker, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var blockerPID int
	if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "report_candidates"); err != nil {
		t.Fatal(err)
	}

	publisher, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Rollback()
	if _, err := publisher.ExecContext(ctx, `UPDATE publications SET visible_after = $1 WHERE article_id = $2`, end.Add(time.Hour), articleID); err != nil {
		t.Fatal(err)
	}
	lockResult := make(chan error, 1)
	go func() {
		_, err := publisher.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext($1))`, "report_candidates")
		lockResult <- err
	}()
	waitForReportLockWaits(t, sqlDB, blockerPID, 1)

	type periodResult struct {
		rows []ReportRow
		err  error
	}
	readerResult := make(chan periodResult, 1)
	go func() {
		rows, err := repository.PeriodReports(ctx, start, end)
		readerResult <- periodResult{rows: rows, err: err}
	}()
	waitForReportLockWaits(t, sqlDB, blockerPID, 2)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-lockResult; err != nil {
		t.Fatalf("publisher did not acquire the release lock: %v", err)
	}
	if err := publisher.Commit(); err != nil {
		t.Fatal(err)
	}
	previous := <-readerResult
	if previous.err != nil {
		t.Fatal(previous.err)
	}
	for _, row := range previous.rows {
		if row.ID == articleID {
			t.Fatal("release committed after the issue cutoff but appeared in the previous issue")
		}
	}
	next, err := repository.PeriodReports(ctx, end, end.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].ID != articleID {
		t.Fatalf("next issue candidates = %#v, want the post-cutoff release", next)
	}
}

func TestSaveReportKeepsRevisionAndReceiptCompletionAtomic(t *testing.T) {
	db := reportTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	key := fmt.Sprintf("report-repo-test-%d", time.Now().UnixNano())
	receiptKey := "report-repo-test:" + key
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM report_revisions WHERE report_id IN (SELECT id FROM reports WHERE key = ?)", key).Error
		_ = db.Exec("DELETE FROM reports WHERE key = ?", key).Error
		_ = db.Exec("DELETE FROM receipts WHERE logical_key = ?", receiptKey).Error
	})
	start := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	first := map[string]any{"metrics": map[string]int{"totalEvents": 2}, "leadItemId": "fixture"}
	written, err := repo.SaveReport(ctx, "daily", key, start, end, first, nil, nil, nil)
	if err != nil || !written {
		t.Fatalf("initial report write = %t, %v", written, err)
	}
	saved, err := repo.SavedReport(ctx, "daily", key)
	if err != nil || saved == nil || saved.Entries != 2 {
		t.Fatalf("saved report = %#v, %v", saved, err)
	}
	var receiptID int64
	if err := db.Raw(`INSERT INTO receipts(logical_key,service,purpose,subject,status,request,attempts,origin)
 VALUES(?, 'fixture', 'report_weekly', 'report:weekly:fixture', 'received', '{}'::jsonb, 1, 'live') RETURNING id`, receiptKey).Scan(&receiptID).Error; err != nil {
		t.Fatal(err)
	}
	reason := "integration fixture"
	model := "fixture-model"
	second := map[string]any{"metrics": map[string]int{"totalEvents": 4}, "leadItemId": "fixture-2"}
	written, err = repo.SaveReport(ctx, "daily", key, start, end.Add(time.Hour), second, &reason, &model, []int64{receiptID})
	if err != nil || !written {
		t.Fatalf("report regeneration = %t, %v", written, err)
	}
	var stored struct {
		Revision int             `gorm:"column:revision"`
		Content  json.RawMessage `gorm:"column:content"`
		Model    *string         `gorm:"column:model"`
	}
	if err := db.Raw("SELECT revision, content, model FROM reports WHERE kind = 'daily' AND key = ?", key).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Revision != 2 || stored.Model == nil || *stored.Model != model {
		t.Fatalf("regenerated report metadata = %#v", stored)
	}
	var revisions int
	if err := db.Raw("SELECT count(*)::int FROM report_revisions WHERE report_id = (SELECT id FROM reports WHERE key = ?)", key).Scan(&revisions).Error; err != nil || revisions != 1 {
		t.Fatalf("revision archive count = %d, %v", revisions, err)
	}
	var receiptStatus string
	if err := db.Raw("SELECT status FROM receipts WHERE id = ?", receiptID).Scan(&receiptStatus).Error; err != nil || receiptStatus != "completed" {
		t.Fatalf("completed model receipt = %q, %v", receiptStatus, err)
	}
}
