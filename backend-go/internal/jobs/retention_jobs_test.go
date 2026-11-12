package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"github.com/riverqueue/river"
)

type retentionServiceStub struct {
	dueDate string
	date    string
	run     int
}

func (s *retentionServiceStub) Due(context.Context, time.Time) (string, bool, error) {
	return s.dueDate, true, nil
}

func (s *retentionServiceStub) Run(_ context.Context, date string, _ time.Time) error {
	s.date = date
	s.run++
	return nil
}

func TestRetentionWorkerDelegatesDateToService(t *testing.T) {
	service := &retentionServiceStub{}
	worker := &RetentionWorker{Service: service}
	job := &river.Job[RetentionArgs]{Args: RetentionArgs{Date: "2026-10-07"}}
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if service.run != 1 || service.date != "2026-10-07" {
		t.Fatalf("retention service calls=%d date=%q", service.run, service.date)
	}
}

func TestRetentionScanEnqueuesOneRiverJobForDueDate(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	client, err := NewClient(ctx, f.pool, f.orm, f.cfg, nil, interests.PublicationSettings{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	date := fmt.Sprintf("test-%d", time.Now().UnixNano())
	service := &retentionServiceStub{dueDate: date}
	worker := &RetentionScanWorker{Service: service, Client: client}
	for range 2 {
		if err := worker.Work(ctx, &river.Job[RetentionScanArgs]{}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='myhot_retention' AND args->>'date'=$1`, date).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retention jobs for %s = %d: %v", date, count, err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM river_job WHERE kind='myhot_retention' AND args->>'date'=$1`, date)
	})
}
