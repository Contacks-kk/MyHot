package adminruns

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOverviewQueriesPgBossAndRiverQueues(t *testing.T) {
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
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	var pgbossExists bool
	if err := orm.Raw("SELECT to_regclass('pgboss.job') IS NOT NULL").Scan(&pgbossExists).Error; err != nil {
		t.Fatal(err)
	}
	if !pgbossExists {
		t.Skip("legacy pg-boss schema is not installed in the local test database")
	}
	if err := orm.Exec(`CREATE TEMP TABLE river_job (
		state text NOT NULL, queue text NOT NULL, created_at timestamptz NOT NULL, finalized_at timestamptz,
		kind text NOT NULL, errors jsonb[]
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO river_job (state,queue,created_at,finalized_at,kind,errors) VALUES
		('available','adminruns-fixture',now()-interval '4 minutes',NULL,'myhot_fixture_available',NULL),
		('scheduled','adminruns-fixture',now()-interval '3 minutes',NULL,'myhot_fixture_scheduled',NULL),
		('retryable','adminruns-fixture',now()-interval '2 minutes',NULL,'myhot_fixture_retry',NULL),
		('running','adminruns-fixture',now()-interval '1 minute',NULL,'myhot_fixture_running',NULL),
		('discarded','default',now()-interval '1 minute',now(),'myhot_fixture_failed',ARRAY['{"error":"fixture failure"}'::jsonb])`).Error; err != nil {
		t.Fatal(err)
	}
	data, err := NewRepository(orm).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]int, len(data.Queues))
	for _, queue := range data.Queues {
		if strings.HasPrefix(queue.Name, "river:") {
			states[queue.State] += queue.N
		}
	}
	if states["created"] != 2 || states["retry"] != 1 || states["active"] != 1 {
		t.Fatalf("River queue states were not mapped to the runs view: %#v", states)
	}
	var riverFailure *FailedJob
	for i := range data.FailedJobs {
		if data.FailedJobs[i].Name == "river:myhot_fixture_failed" {
			riverFailure = &data.FailedJobs[i]
			break
		}
	}
	if riverFailure == nil || riverFailure.Failed != 1 || riverFailure.LastOutput == nil || !strings.Contains(*riverFailure.LastOutput, "fixture failure") {
		t.Fatalf("discarded River job was not reported as a failure: %#v", data.FailedJobs)
	}
}

func TestRequeueFailedUpdatesAndAuditsAtomically(t *testing.T) {
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
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	setup := []string{
		`CREATE TEMP TABLE articles (
			id text PRIMARY KEY, processing_state text NOT NULL, processing_attempts integer NOT NULL,
			processing_retry_at timestamptz, processing_error text, discovered_at timestamptz NOT NULL,
			processing_queued_at timestamptz
		)`,
		`CREATE TEMP TABLE audit_log (
			id bigserial PRIMARY KEY, actor text NOT NULL, action text NOT NULL, subject text, reason text,
			"before" jsonb NOT NULL, "after" jsonb NOT NULL, request_id text, created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`INSERT INTO articles (id, processing_state, processing_attempts, processing_retry_at, processing_error, discovered_at, processing_queued_at) VALUES
			('recent', 'failed', 4, now(), 'provider timeout 123456', now() - interval '1 day', now()),
			('other', 'failed', 2, now(), 'other issue', now() - interval '1 day', now()),
			('old', 'failed', 3, now(), 'provider timeout 654321', now() - interval '31 days', now()),
			('already-new', 'new', 2, now(), 'provider timeout 123456', now() - interval '1 day', now())`,
	}
	for _, statement := range setup {
		if err := orm.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	repository := NewRepository(orm)
	group := "provider timeout …"
	result, err := repository.RequeueFailed(context.Background(), &group, "retry after review", "admin:41", "failure:"+group)
	if err != nil || result.Requeued != 1 {
		t.Fatalf("selected failed group was not reset: result=%#v err=%v", result, err)
	}
	var audit struct {
		Actor    string
		Action   string
		Subject  string
		Reason   string
		Requeued int
	}
	if err := orm.Raw(`SELECT actor, action, subject, reason, ("after"->>'requeued')::int AS requeued FROM audit_log`).Scan(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.Actor != "admin:41" || audit.Action != "processing.requeue" || audit.Subject != "failure:"+group || audit.Reason != "retry after review" || audit.Requeued != 1 {
		t.Fatalf("requeue audit contract changed: %#v", audit)
	}
	var reset struct {
		State    string
		Attempts int
		RetryAt  *time.Time
		Error    *string
		QueuedAt *time.Time
	}
	if err := orm.Raw(`SELECT processing_state AS state, processing_attempts AS attempts, processing_retry_at AS retry_at,
		processing_error AS error, processing_queued_at AS queued_at FROM articles WHERE id = 'recent'`).Scan(&reset).Error; err != nil {
		t.Fatal(err)
	}
	if reset.State != "new" || reset.Attempts != 0 || reset.RetryAt != nil || reset.Error != nil || reset.QueuedAt != nil {
		t.Fatalf("requeued article was not made sweepable: %#v", reset)
	}

	if err := orm.Exec(`INSERT INTO articles (id, processing_state, processing_attempts, processing_retry_at, processing_error, discovered_at, processing_queued_at)
		VALUES ('rollback', 'failed', 3, now(), 'rollback failure 999999', now() - interval '1 day', now())`).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`ALTER TABLE audit_log ADD CONSTRAINT reject_rollback_actor CHECK (actor <> 'rollback')`).Error; err != nil {
		t.Fatal(err)
	}
	rollbackGroup := "rollback failure …"
	if _, err := repository.RequeueFailed(context.Background(), &rollbackGroup, "retry", "rollback", "failure:rollback"); err == nil {
		t.Fatal("audit failure did not fail the requeue transaction")
	}
	var state string
	if err := orm.Raw(`SELECT processing_state FROM articles WHERE id = 'rollback'`).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("article update escaped failed audit transaction: state=%q", state)
	}
}
