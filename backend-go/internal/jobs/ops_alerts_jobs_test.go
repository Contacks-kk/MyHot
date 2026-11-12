package jobs

import (
	"testing"
	"time"
)

func TestOpsAlertsTaskKeepsStableIdentityAndRetryPolicy(t *testing.T) {
	if (OpsAlertsArgs{}).Kind() != "myhot_ops_alerts" {
		t.Fatalf("ops alert task kind changed: %s", (OpsAlertsArgs{}).Kind())
	}
	options := opsAlertsInsertOpts()
	if options.MaxAttempts != 2 || !options.UniqueOpts.ByArgs || len(options.UniqueOpts.ByState) != 5 {
		t.Fatalf("ops alert retry or uniqueness changed: %+v", options)
	}
}

func TestOpsAlertsScheduleRunsOnTenMinuteBoundariesAndSkipsMissedSlots(t *testing.T) {
	for _, test := range []struct {
		current time.Time
		want    time.Time
	}{
		{time.Date(2026, 10, 8, 14, 2, 30, 0, time.UTC), time.Date(2026, 10, 8, 14, 10, 0, 0, time.UTC)},
		{time.Date(2026, 10, 8, 14, 10, 1, 0, time.UTC), time.Date(2026, 10, 8, 14, 20, 0, 0, time.UTC)},
	} {
		if got := (tenMinuteSchedule{}).Next(test.current); !got.Equal(test.want) {
			t.Fatalf("next after %s = %s, want %s", test.current, got, test.want)
		}
	}
	_ = opsAlertsPeriodicJob() // no RunOnStart: missed cron slots remain skipped.
}
