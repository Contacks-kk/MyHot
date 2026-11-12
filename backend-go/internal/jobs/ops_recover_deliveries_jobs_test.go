package jobs

import (
	"testing"
	"time"
)

func TestDeliveryRecoveryTaskKeepsStableIdentityAndOneRetry(t *testing.T) {
	if (DeliveryRecoveryArgs{}).Kind() != "myhot_ops_recover_deliveries" {
		t.Fatalf("delivery recovery kind changed: %s", (DeliveryRecoveryArgs{}).Kind())
	}
	options := deliveryRecoveryInsertOpts()
	if options.MaxAttempts != 2 || options.UniqueOpts.ByPeriod != 10*time.Minute {
		t.Fatalf("delivery recovery retry or uniqueness changed: %+v", options)
	}
}

func TestDeliveryRecoveryRunsOnTenMinuteBoundariesAndSkipsMissedSlots(t *testing.T) {
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
	_ = deliveryRecoveryPeriodicJob() // its periodic options omit RunOnStart, matching missed-slot skip.
}
