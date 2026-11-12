package adminops

import (
	"context"
	"testing"
)

type memoryRepository struct {
	filter AuditFilter
}

func (r *memoryRepository) NavCounts(context.Context) (NavCounts, error) {
	return NavCounts{Feedback: 2, Sources: 1, Runs: 3}, nil
}
func (r *memoryRepository) Audit(_ context.Context, filter AuditFilter) (AuditResult, error) {
	r.filter = filter
	return AuditResult{Page: filter.Page, Rows: []AuditRow{}}, nil
}

func TestAuditNormalizesPageAndPreservesOptionalFilters(t *testing.T) {
	repo := &memoryRepository{}
	service := NewService(repo)
	subject, action := "feedback:7", "feedback."
	result, err := service.Audit(context.Background(), AuditFilter{Subject: &subject, Action: &action, Page: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Page != 1 || repo.filter.Page != 1 || repo.filter.Subject != &subject || repo.filter.Action != &action {
		t.Fatalf("audit filter normalization changed: result=%#v filter=%#v", result, repo.filter)
	}
}

func TestNavCountsReturnStableKeys(t *testing.T) {
	counts, err := NewService(&memoryRepository{}).NavCounts(context.Background())
	if err != nil || counts.Feedback != 2 || counts.Sources != 1 || counts.Runs != 3 {
		t.Fatalf("unexpected navigation counts: %#v err=%v", counts, err)
	}
}
