package jobs

import (
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestSourceHealthTaskKeepsStableIdentityAndSingleRetry(t *testing.T) {
	if (SourceHealthScanArgs{}).Kind() != "myhot_source_health_scan" || (SourceHealthArgs{}).Kind() != "myhot_source_health_report" {
		t.Fatal("source-health River job kind changed")
	}
	options := sourceHealthInsertOpts()
	if options.MaxAttempts != 2 || !options.UniqueOpts.ByArgs || len(options.UniqueOpts.ByState) != 5 {
		t.Fatalf("source-health work must be unique and allow one automatic retry: %#v", options)
	}
	if options.UniqueOpts.ByState[0] != rivertype.JobStateAvailable || options.UniqueOpts.ByState[3] != rivertype.JobStateRetryable {
		t.Fatalf("source-health uniqueness no longer covers runnable/retryable jobs: %#v", options.UniqueOpts.ByState)
	}
}
