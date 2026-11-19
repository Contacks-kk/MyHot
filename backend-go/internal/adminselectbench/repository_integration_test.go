package adminselectbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIntegrationRepository(t *testing.T) (*gorm.DB, *GormRepository) {
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
	return orm, NewRepository(orm)
}

func TestImportListAndFilteredRunRoundTrip(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	ctx := context.Background()
	id := fmt.Sprintf("selectbench_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = orm.Where("run_id = ?", id).Delete(&model.SelectBenchResult{}).Error
		_ = orm.Where("id = ?", id).Delete(&model.SelectBenchRun{}).Error
		_ = orm.Where("subject = ?", "selectbench:"+id).Delete(&model.AdminAuditLog{}).Error
	})

	data := ImportData{ID: id, Label: "repository fixture", SampleSize: 1, Models: []string{"a", "b"}, Summary: json.RawMessage(`{"a":{"n":1,"sweep":[]},"b":{"n":1,"sweep":[]}}`), Results: []ImportResult{
		{Model: "a", CaseID: "case-1", Title: "fixture title", Stratum: stringPtr("policy"), Gold: "reject", Decision: stringPtr("select")},
		{Model: "b", CaseID: "case-1", Title: "fixture title", Stratum: stringPtr("policy"), Gold: "reject", Decision: stringPtr("reject")},
	}}
	if err := repository.Import(ctx, data, "admin:test"); err != nil {
		t.Fatal(err)
	}

	runs, err := repository.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, run := range runs {
		if run.ID != id {
			continue
		}
		found = true
		if run.Cases != 2 || run.ImportedBy == nil || *run.ImportedBy != "admin:test" || strings.Contains(string(run.Summary), `"sweep"`) {
			t.Fatalf("list response changed: %#v", run)
		}
	}
	if !found {
		t.Fatalf("imported run %q not listed", id)
	}

	detail, err := repository.Run(ctx, id, Filters{Model: "a", Outcome: "fp", Stratum: stringPtr("policy"), Disagree: true})
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || detail.Run.ID != id || detail.Run.Seed != nil || len(detail.Rows) != 1 || detail.Rows[0].CaseID != "case-1" || detail.Rows[0].ByModel["a"].Decision == nil || *detail.Rows[0].ByModel["a"].Decision != "select" || len(detail.Strata) != 1 || detail.Strata[0].N != 1 {
		t.Fatalf("filtered result changed: %#v", detail)
	}
}

func TestSelectBenchImportRollsBackWhenAuditInsertFails(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	id := fmt.Sprintf("selectbench_rollback_%d", time.Now().UnixNano())
	constraint := fmt.Sprintf("selectbench_audit_fail_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = orm.Exec("ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS " + constraint).Error
		_ = orm.Where("run_id = ?", id).Delete(&model.SelectBenchResult{}).Error
		_ = orm.Where("id = ?", id).Delete(&model.SelectBenchRun{}).Error
	})
	if err := orm.Exec("ALTER TABLE audit_log ADD CONSTRAINT " + constraint + " CHECK (action <> 'selectbench.import') NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	data := ImportData{ID: id, Label: "rollback fixture", Models: []string{"a"}, Summary: json.RawMessage(`{"a":{"sweep":[]}}`)}
	err := repository.Import(context.Background(), data, "admin:test")
	if err == nil {
		t.Fatal("expected audit constraint to fail")
	}
	var count int64
	if err := orm.Model(&model.SelectBenchRun{}).Where("id = ?", id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("run survived failed audit transaction: count=%d", count)
	}
}

/** TestIdempotentEvaluationImportSurvivesWorkerRetry checks the transactional retry boundary. */
func TestIdempotentEvaluationImportSurvivesWorkerRetry(t *testing.T) {
	orm, repository := openIntegrationRepository(t)
	id := fmt.Sprintf("selectbench_eval_retry_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = orm.Where("run_id = ?", id).Delete(&model.SelectBenchResult{}).Error
		_ = orm.Where("id = ?", id).Delete(&model.SelectBenchRun{}).Error
		_ = orm.Where("subject = ?", "selectbench:"+id).Delete(&model.AdminAuditLog{}).Error
	})
	data := ImportData{
		ID:         id,
		Idempotent: true,
		Label:      "retry fixture",
		Models:     []string{"model"},
		Summary:    json.RawMessage(`{"model":{"n":1,"sweep":[]}}`),
		Results: []ImportResult{{
			Model: "model", CaseID: "case-1", Title: "fixture title", Gold: "select", Decision: stringPtr("select"),
		}},
	}
	for range 2 {
		if err := repository.Import(context.Background(), data, "script:eval-selection"); err != nil {
			t.Fatal(err)
		}
	}
	var runs, results, audits int64
	if err := orm.Model(&model.SelectBenchRun{}).Where("id = ?", id).Count(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Model(&model.SelectBenchResult{}).Where("run_id = ?", id).Count(&results).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Model(&model.AdminAuditLog{}).Where("subject = ? AND action = ?", "selectbench:"+id, "selectbench.import").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if runs != 1 || results != 1 || audits != 1 {
		t.Fatalf("worker retry duplicated SelectBench data: runs=%d results=%d audits=%d", runs, results, audits)
	}
}
