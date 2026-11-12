package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

type feedbackForwardServiceFixture struct {
	ids       []int64
	forwarded []int64
}

func (s *feedbackForwardServiceFixture) PendingForwardIDs(context.Context, time.Time) ([]int64, error) {
	return s.ids, nil
}

func (s *feedbackForwardServiceFixture) Forward(_ context.Context, id int64) error {
	s.forwarded = append(s.forwarded, id)
	return nil
}

func TestFeedbackForwardUsesStableRiverKindAndBoundedRetry(t *testing.T) {
	if (FeedbackScanArgs{}).Kind() != "myhot_feedback_scan" || (FeedbackForwardArgs{}).Kind() != "myhot_feedback_forward" {
		t.Fatal("feedback River job kind changed")
	}
	opts := FeedbackForwardInsertOpts()
	if opts.MaxAttempts != 2 || !opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) != 5 {
		t.Fatalf("feedback forwarding must be unique with one automatic retry: %#v", opts)
	}
}

func TestTenMinuteScheduleUsesNextWallClockBoundary(t *testing.T) {
	schedule := tenMinuteSchedule{}
	for _, test := range []struct {
		current string
		want    string
	}{
		{"2026-10-07T08:00:00Z", "2026-10-07T08:10:00Z"},
		{"2026-10-07T08:09:59Z", "2026-10-07T08:10:00Z"},
		{"2026-10-07T08:59:59Z", "2026-10-07T09:00:00Z"},
	} {
		current, err := time.Parse(time.RFC3339, test.current)
		if err != nil {
			t.Fatal(err)
		}
		if got := schedule.Next(current).Format(time.RFC3339); got != test.want {
			t.Fatalf("next cron time after %s = %s, want %s", test.current, got, test.want)
		}
	}
}

func TestFeedbackForwardWorkerDelegatesToService(t *testing.T) {
	service := &feedbackForwardServiceFixture{}
	worker := &FeedbackForwardWorker{Service: service}
	if err := worker.Work(context.Background(), &river.Job[FeedbackForwardArgs]{Args: FeedbackForwardArgs{FeedbackID: 27}}); err != nil {
		t.Fatal(err)
	}
	if len(service.forwarded) != 1 || service.forwarded[0] != 27 {
		t.Fatalf("forward worker did not use feedback service: %#v", service.forwarded)
	}
}
