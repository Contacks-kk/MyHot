package jobs

import (
	"testing"
	"time"
)

func TestReceiptRecoveryTaskKeepsStableIdentityAndOneTenMinuteSchedule(t *testing.T) {
	if (ReceiptRecoveryArgs{}).Kind() != "myhot_ops_recover_receipts" {
		t.Fatalf("receipt recovery kind changed: %s", (ReceiptRecoveryArgs{}).Kind())
	}
	options := receiptRecoveryInsertOpts()
	if options.MaxAttempts != 2 || options.UniqueOpts.ByPeriod != 10*time.Minute {
		t.Fatalf("receipt recovery retries/uniqueness changed: %+v", options)
	}
	current := time.Date(2026, 10, 8, 3, 2, 0, 0, time.UTC)
	if next := (tenMinuteSchedule{}).Next(current); !next.Equal(time.Date(2026, 10, 8, 3, 10, 0, 0, time.UTC)) {
		t.Fatalf("next ten minute boundary = %s", next)
	}
}
