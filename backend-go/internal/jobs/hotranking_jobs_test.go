package jobs

import (
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestHotRankingTaskKeepsStableIdentityAndOneAutomaticRetry(t *testing.T) {
	if (HotRankingArgs{}).Kind() != "myhot_hot_ranking" {
		t.Fatal("hot-ranking River job kind changed")
	}
	options := hotRankingInsertOpts()
	if options.MaxAttempts != 2 || !options.UniqueOpts.ByArgs || len(options.UniqueOpts.ByState) != 5 {
		t.Fatalf("hot-ranking task must be unique while queued/running and permit one retry: %#v", options)
	}
	if options.UniqueOpts.ByState[0] != rivertype.JobStateAvailable || options.UniqueOpts.ByState[3] != rivertype.JobStateRetryable {
		t.Fatalf("hot-ranking uniqueness no longer covers runnable/retryable jobs: %#v", options.UniqueOpts.ByState)
	}
	if hotRankingPeriodicOptions().RunOnStart {
		t.Fatal("the hot-ranking cron must skip missed intervals after a restart")
	}
}
