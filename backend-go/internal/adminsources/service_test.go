package adminsources

import (
	"context"
	"testing"
)

type readOnlyRepository struct {
	filter Filter
	result ListResult
}

func (r *readOnlyRepository) List(_ context.Context, filter Filter) (ListResult, error) {
	r.filter = filter
	return r.result, nil
}

func (*readOnlyRepository) Detail(context.Context, string) (*Detail, error) { return nil, nil }

func TestListNormalizesPageAndKeepsFilters(t *testing.T) {
	repo := &readOnlyRepository{}
	query, kind := "%ai%", "rss"
	result, err := NewService(repo).List(context.Background(), Filter{Page: 0, Query: &query, Kind: &kind})
	if err != nil || result.Page != 1 || repo.filter.Page != 1 || repo.filter.Query != &query || repo.filter.Kind != &kind || result.Rows == nil {
		t.Fatalf("filters or stable empty rows changed: result=%#v filter=%#v err=%v", result, repo.filter, err)
	}
}
