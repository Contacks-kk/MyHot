package adminruns

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	data Data
	err  error
}

func (r memoryRepository) Load(context.Context) (Data, error) { return r.data, r.err }

type requeueMemoryRepository struct {
	memoryRepository
	group   *string
	reason  string
	actor   string
	subject string
	called  bool
	err     error
}

func (r *requeueMemoryRepository) RequeueFailed(_ context.Context, group *string, reason, actor, subject string) (RequeueResult, error) {
	r.group, r.reason, r.actor, r.subject, r.called = group, reason, actor, subject, true
	return RequeueResult{Requeued: 3}, r.err
}

func TestRequeueFailedValidatesAndPreservesAuditInputs(t *testing.T) {
	group := strings.Repeat("x", 81)
	repository := &requeueMemoryRepository{}
	service := NewService(repository)
	for _, reason := range []string{"", " \t\r\n", "\ufeff"} {
		if _, err := service.RequeueFailed(context.Background(), &group, reason, "admin:41"); !errors.Is(err, ErrInvalidRequeue) {
			t.Fatalf("reason %q error = %v, want ErrInvalidRequeue", reason, err)
		}
	}
	if repository.called {
		t.Fatal("invalid requeue reached the repository")
	}

	result, err := service.RequeueFailed(context.Background(), &group, " reviewed ", "admin:41")
	if err != nil || result.Requeued != 3 || !repository.called || repository.group == nil || *repository.group != group || repository.reason != " reviewed " || repository.actor != "admin:41" || repository.subject != "failure:"+strings.Repeat("x", 80) {
		t.Fatalf("requeue inputs/result changed: result=%#v repository=%#v err=%v", result, repository, err)
	}

	emptyGroup := ""
	if _, err := service.RequeueFailed(context.Background(), &emptyGroup, "valid", "admin:41"); err != nil || repository.subject != "failure:all" || repository.group == nil || *repository.group != "" {
		t.Fatalf("empty group compatibility changed: group=%v subject=%q err=%v", repository.group, repository.subject, err)
	}
	if _, err := service.RequeueFailed(context.Background(), nil, "valid", "admin:41"); err != nil || repository.subject != "failure:all" || repository.group != nil {
		t.Fatalf("all failures compatibility changed: group=%v subject=%q err=%v", repository.group, repository.subject, err)
	}
}

func TestRequeueFailedPropagatesRepositoryError(t *testing.T) {
	want := errors.New("database unavailable")
	service := NewService(&requeueMemoryRepository{err: want})
	if _, err := service.RequeueFailed(context.Background(), nil, "reason", "admin:41"); !errors.Is(err, want) {
		t.Fatalf("repository error = %v, want %v", err, want)
	}
}

func TestOverviewKeepsHeartbeatAndGroupingRecoverySemantics(t *testing.T) {
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	status := func(value string) *string { return &value }
	data := Data{
		Heartbeats: []HeartbeatRow{
			{Key: "heartbeat.worker", Value: []byte(`{"pid":12,"host":"local","release":"r1","startedAt":"2026-10-07T02:00:00Z"}`), UpdatedAt: now.Add(-time.Minute)},
			{Key: "heartbeat.old", Value: []byte(`{"pid":13,"host":"old","release":"r0","startedAt":"2026-10-01T00:00:00Z"}`), UpdatedAt: now.Add(-4 * time.Minute)},
		},
		Grouping: []WaitingGroup{
			{GroupItem: GroupItem{ArticleID: "a", Since: now.Add(-11 * time.Minute)}, ReceiptStatus: status("unknown")},
			{GroupItem: GroupItem{ArticleID: "b", Since: now.Add(-time.Minute), Failed: true}, ReceiptStatus: status("unknown"), Released: true},
			{GroupItem: GroupItem{ArticleID: "c", Since: now.Add(-time.Minute), Failed: true}, ReceiptStatus: status("pending")},
			{GroupItem: GroupItem{ArticleID: "d", Since: now.Add(-time.Minute), Failed: true}},
		},
		Receipts: []ReceiptCount{{Status: "unknown", N: 2}},
	}
	result, err := NewService(memoryRepository{data: data}).Overview(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Processes) != 2 || result.Processes[0].Role != "worker" || !result.Processes[0].Alive || result.Processes[1].Alive {
		t.Fatalf("heartbeat liveness changed: %#v", result.Processes)
	}
	if result.Grouping.Waiting != 4 || result.Grouping.NeedsAttention != 1 || len(result.Grouping.Items) != 4 {
		t.Fatalf("grouping counts changed: %#v", result.Grouping)
	}
	got := []string{result.Grouping.Items[0].Recovery, result.Grouping.Items[1].Recovery, result.Grouping.Items[2].Recovery, result.Grouping.Items[3].Recovery}
	want := []string{"receipt", "manual", "automatic", "manual"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recovery[%d]=%q, want %q", i, got[i], want[i])
		}
	}
	if result.Receipts.Counts["unknown"] != 2 || result.Modules == nil || result.Queues == nil || result.Ingest == nil {
		t.Fatalf("empty and receipt response contract changed: %#v", result)
	}
}

func TestOverviewPropagatesRepositoryAndHeartbeatErrors(t *testing.T) {
	if _, err := NewService(memoryRepository{err: errors.New("database unavailable")}).Overview(context.Background(), time.Now()); err == nil {
		t.Fatal("repository error was hidden")
	}
	data := Data{Heartbeats: []HeartbeatRow{{Key: "heartbeat.worker", Value: []byte("invalid"), UpdatedAt: time.Now()}}}
	if _, err := NewService(memoryRepository{data: data}).Overview(context.Background(), time.Now()); err == nil {
		t.Fatal("invalid heartbeat JSON was accepted")
	}
}

func TestGroupingOverviewSupportsLegacyAndRiverQueueRetirement(t *testing.T) {
	legacyOnly := groupingOverviewSQL(true, false)
	if !strings.Contains(legacyOnly, "pgboss.job") || !strings.Contains(legacyOnly, "content.rerun.group") {
		t.Fatalf("legacy grouping view lost its pending-job or rerun data: %s", legacyOnly)
	}
	riverOnly := groupingOverviewSQL(false, true)
	if strings.Contains(riverOnly, "pgboss") || !strings.Contains(riverOnly, "river_job") || !strings.Contains(riverOnly, "content.rerun.group") {
		t.Fatalf("River-only grouping view still depends on pg-boss: %s", riverOnly)
	}
	withoutQueues := groupingOverviewSQL(false, false)
	if strings.Contains(withoutQueues, "pgboss") || strings.Contains(withoutQueues, "river_job") || !strings.Contains(withoutQueues, "(false) AS running") {
		t.Fatalf("grouping overview without queue tables is invalid: %s", withoutQueues)
	}
}
