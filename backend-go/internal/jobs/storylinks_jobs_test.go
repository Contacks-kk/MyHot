package jobs

import (
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestStoryLinksTaskKeepsStableIdentityAndOneAutomaticRetry(t *testing.T) {
	if (StoryLinksArgs{}).Kind() != "myhot_story_links" {
		t.Fatal("story-links River job kind changed")
	}
	options := storyLinksInsertOpts()
	if options.MaxAttempts != 2 || !options.UniqueOpts.ByArgs || len(options.UniqueOpts.ByState) != 5 {
		t.Fatalf("story-links task must be unique while queued/running and permit one retry: %#v", options)
	}
	if options.UniqueOpts.ByState[0] != rivertype.JobStateAvailable || options.UniqueOpts.ByState[3] != rivertype.JobStateRetryable {
		t.Fatalf("story-links uniqueness no longer covers runnable/retryable jobs: %#v", options.UniqueOpts.ByState)
	}
}
