package adminselectbench

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type memoryRepository struct {
	imported ImportData
	actor    string
	called   bool
}

func (r *memoryRepository) List(context.Context) ([]RunListEntry, error)          { return nil, nil }
func (*memoryRepository) Run(context.Context, string, Filters) (*RunCases, error) { return nil, nil }
func (r *memoryRepository) Import(_ context.Context, data ImportData, actor string) error {
	r.imported, r.actor, r.called = data, actor, true
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

type errorRepository struct{ memoryRepository }

func (*errorRepository) List(context.Context) ([]RunListEntry, error) {
	return nil, errors.New("database unavailable")
}

func TestListReturnsRepositoryError(t *testing.T) {
	if _, err := NewService(&errorRepository{}).List(context.Background()); err == nil {
		t.Fatal("expected repository error")
	}
}
