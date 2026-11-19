package adminselectbench

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aihot/backend-go/internal/editorial"
	"aihot/backend-go/internal/receipts"
)

type memoryRepository struct {
	imported ImportData
	actor    string
	called   bool
	imports  int
}

func (r *memoryRepository) List(context.Context) ([]RunListEntry, error)          { return nil, nil }
func (*memoryRepository) Run(context.Context, string, Filters) (*RunCases, error) { return nil, nil }
func (r *memoryRepository) Import(_ context.Context, data ImportData, actor string) error {
	if data.Idempotent && r.called && r.imported.ID == data.ID {
		return nil
	}
	r.imported, r.actor, r.called = data, actor, true
	r.imports++
	return nil
}

func TestImportPreservesReportModelOrderAndBatchesItsData(t *testing.T) {
	repo := &memoryRepository{}
	service := NewService(repo)
	const raw = `{"meta":{"split":"test","n":2,"seed":7,"promptVersion":"v3"},"models":{"second":{"summary":{"n":2,"accuracy":0.5},"sweep":[],"cases":[{"caseId":"case-2","title":"Second","gold":"reject","decision":"reject","receiptId":9}]},"first":{"summary":{"n":2},"cases":[{"caseId":"case-1","title":"First","gold":"select","decision":"select"}]}}}`
	id, err := service.Import(context.Background(), "fixture", json.RawMessage(raw), "admin:7")
	if err != nil {
		t.Fatal(err)
	}
	if !repo.called || repo.actor != "admin:7" || repo.imported.Label != "fixture" || repo.imported.Split == nil || *repo.imported.Split != "test" || repo.imported.SampleSize != 2 || repo.imported.Seed == nil || *repo.imported.Seed != 7 || repo.imported.Prompt == nil || *repo.imported.Prompt != "v3" {
		t.Fatalf("import metadata was not passed through: %#v actor=%q", repo.imported, repo.actor)
	}
	if id != repo.imported.ID || len(repo.imported.Models) != 2 || repo.imported.Models[0] != "second" || repo.imported.Models[1] != "first" || len(repo.imported.Results) != 2 {
		t.Fatalf("report order or rows changed: id=%q data=%#v", id, repo.imported)
	}
	var summary map[string]map[string]json.RawMessage
	if err := json.Unmarshal(repo.imported.Summary, &summary); err != nil || len(summary["second"]["sweep"]) == 0 {
		t.Fatalf("sweep data was not kept in the stored summary: %s err=%v", repo.imported.Summary, err)
	}
}

func TestImportRejectsReportsWithoutModelSummaries(t *testing.T) {
	repo := &memoryRepository{}
	_, err := NewService(repo).Import(context.Background(), "fixture", json.RawMessage(`{"models":{"empty":{"cases":[]}}}`), "admin:7")
	if err == nil || err.Error() != "report has no model summaries" || repo.called {
		t.Fatalf("invalid report reached repository: called=%v err=%v", repo.called, err)
	}
}

type selectionReportUsage struct{}

/** EvaluationUsage supplies deterministic receipt totals for the import contract. */
func (selectionReportUsage) EvaluationUsage(context.Context, []int64) (receipts.EvaluationUsage, error) {
	return receipts.EvaluationUsage{TokensIn: 10}, nil
}

/** TestImportAcceptsGoSelectionReport checks the generated report against the real import service. */
func TestImportAcceptsGoSelectionReport(t *testing.T) {
	rows, err := editorial.ParseSelectionGold(`{"caseId":"one","material":{"title":"Event","sourceName":"Source"},"sourceFacts":{"sourceKind":"rss"},"gold":{"decision":"select"}}`)
	if err != nil {
		t.Fatal(err)
	}
	score := 65
	result := editorial.SelectionEvaluationResult{
		Row:        rows[0],
		Output:     &editorial.AnalysisOutput{Selected: true, Relevance: "pass", Score: &score},
		ReceiptIDs: []int64{11},
	}
	model, err := editorial.BuildSelectionModelReport(context.Background(), selectionReportUsage{}, "model", []editorial.SelectionEvaluationResult{result}, 40, 42, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"meta": map[string]any{
			"split":         "all",
			"n":             1,
			"seed":          7,
			"promptVersion": "v1",
		},
		"models": map[string]editorial.SelectionModelReport{"model": model},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &memoryRepository{}
	service := NewService(repo)
	id, err := service.ImportWithID(context.Background(), "sb-eval-123", "fixture", raw, "script:eval-selection")
	if err != nil {
		t.Fatal(err)
	}
	retryID, err := service.ImportWithID(context.Background(), "sb-eval-123", "fixture", raw, "script:eval-selection")
	if err != nil || id != retryID || repo.imports != 1 {
		t.Fatalf("retry duplicated SelectBench run: first=%q retry=%q imports=%d err=%v", id, retryID, repo.imports, err)
	}
	if len(repo.imported.Results) != 1 || repo.imported.Results[0].Decision == nil || *repo.imported.Results[0].Decision != "select" || repo.imported.Results[0].ReceiptID == nil || *repo.imported.Results[0].ReceiptID != 11 || repo.imported.SampleSize != 1 {
		t.Fatalf("generated report did not retain case fields: %#v", repo.imported)
	}
}

type errorRepository struct{ memoryRepository }

func (*errorRepository) List(context.Context) ([]RunListEntry, error) {
	return nil, errors.New("database unavailable")
}

func TestListReturnsRepositoryError(t *testing.T) {
	if _, err := NewService(&errorRepository{}).List(context.Background()); err == nil {
		t.Fatal("expected repository error")
	}
}
