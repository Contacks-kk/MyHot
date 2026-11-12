package receiptrecovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type memoryRepository struct {
	status        string
	found         bool
	releaseIDs    []int64
	markCutoff    time.Time
	dueCutoff     time.Time
	limit         int
	markCount     int64
	releaseResult map[int64]*Result
	statusErr     error
	releaseErr    error
}

func (r *memoryRepository) Status(context.Context, int64) (string, bool, error) {
	return r.status, r.found, r.statusErr
}
func (r *memoryRepository) Release(_ context.Context, id int64, _, _, _ string, _ *bool) (*Result, bool, error) {
	if r.releaseErr != nil {
		return nil, true, r.releaseErr
	}
	result := r.releaseResult[id]
	return result, result != nil, nil
}
func (r *memoryRepository) MarkStalePending(_ context.Context, cutoff time.Time) (int64, error) {
	r.markCutoff = cutoff
	return r.markCount, nil
}
func (r *memoryRepository) DueUnknown(_ context.Context, cutoff time.Time, limit int) ([]int64, error) {
	r.dueCutoff, r.limit = cutoff, limit
	return r.releaseIDs, nil
}

func TestReleaseManualValidatesAndConflicts(t *testing.T) {
	repo := &memoryRepository{status: "completed", found: true}
	service := NewService(repo)
	if _, _, err := service.ReleaseManual(context.Background(), 1, false, "\ufeff \u3000", "admin:41"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty note error = %v", err)
	}
	if _, _, err := service.ReleaseManual(context.Background(), 1, false, "reviewed", "admin:41"); !errors.Is(err, ErrNotUnknown) {
		t.Fatalf("non-unknown status error = %v", err)
	}
	if _, _, err := service.ReleaseManual(context.Background(), 0, false, "reviewed", "admin:41"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid id error = %v", err)
	}
}

func TestRecoverKeepsThirtyMinuteCutoffAndCapsBatch(t *testing.T) {
	now := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	repo := &memoryRepository{markCount: 2, releaseIDs: []int64{7, 8}, releaseResult: map[int64]*Result{
		7: {ID: 7, Requeued: true}, 8: {ID: 8},
	}}
	counts, err := NewService(repo).Recover(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if counts != (RecoveryCounts{Stale: 2, Released: 2, Requeued: 1}) {
		t.Fatalf("counts = %+v", counts)
	}
	if !repo.markCutoff.Equal(now.Add(-StalePendingAfter)) || !repo.dueCutoff.Equal(now.Add(-AutoReleaseAfter)) || repo.limit != 200 {
		t.Fatalf("cutoffs = stale %s due %s limit %d", repo.markCutoff, repo.dueCutoff, repo.limit)
	}
	if !reflect.DeepEqual(repo.releaseIDs, []int64{7, 8}) {
		t.Fatalf("released IDs = %v", repo.releaseIDs)
	}
}
