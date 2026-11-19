package jobs

import (
	"encoding/json"
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestSharedPipelineRiverKindsPreserveLegacyQueueContracts(t *testing.T) {
	if kind := (ContentAnalyzeArgs{}).Kind(); kind != "content.analyze" {
		t.Fatalf("analysis job kind = %q", kind)
	}
	encoded, err := json.Marshal(ContentAnalyzeArgs{ArticleID: "article-1", AttemptTag: "rerun-2"})
	if err != nil || string(encoded) != `{"articleId":"article-1","attemptTag":"rerun-2"}` {
		t.Fatalf("analysis args = %s err=%v", encoded, err)
	}
	if kind := (EventGroupArgs{}).Kind(); kind != "events.group" {
		t.Fatalf("grouping job kind = %q", kind)
	}
	encoded, err = json.Marshal(EventGroupArgs{ArticleID: "article-2", SignalOnly: true})
	if err != nil || string(encoded) != `{"articleId":"article-2","signalOnly":true}` {
		t.Fatalf("signal grouping args = %s err=%v", encoded, err)
	}
	encoded, err = json.Marshal(EventGroupArgs{ArticleID: "article-3"})
	if err != nil || string(encoded) != `{"articleId":"article-3"}` {
		t.Fatalf("editorial grouping args = %s err=%v", encoded, err)
	}
}

/** TestSignalRevisitUsesLowPriorityUniqueGroupingJobs keeps revived discussion work behind reports. */
func TestSignalRevisitUsesLowPriorityUniqueGroupingJobs(t *testing.T) {
	opts := eventGroupInsertOpts(-1)
	if opts.Queue != "shared_grouping" || opts.Priority != -1 || opts.MaxAttempts != 5 || !opts.UniqueOpts.ByArgs {
		t.Fatalf("signal revisit job options changed: %+v", opts)
	}
	states := map[rivertype.JobState]bool{}
	for _, state := range opts.UniqueOpts.ByState {
		states[state] = true
	}
	for _, state := range []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled} {
		if !states[state] {
			t.Fatalf("signal revisit no longer deduplicates queued state %q: %+v", state, opts.UniqueOpts.ByState)
		}
	}
}

func TestArticleMediaRenditionsMatchSignedProxyURLsAndDeduplicate(t *testing.T) {
	answers := []any{
		map[string]string{"body": `<img src="/api/img-proxy?u=https%3A%2F%2Fimg.example%2Fa.png&mode=thumb">`},
		map[string]string{"body": `<img src="/api/img-proxy?u=https%3A%2F%2Fimg.example%2Fa.png&amp;mode=thumb"><img src="/api/img-proxy?u=https%3A%2F%2Fimg.example%2Fb.gif&amp;mode=full">`},
	}
	pairs, err := proxiedRenditions(answers)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0] != (proxiedRendition{URL: "https://img.example/a.png", Mode: "thumb"}) || pairs[1] != (proxiedRendition{URL: "https://img.example/b.gif", Mode: "full"}) {
		t.Fatalf("renditions = %#v", pairs)
	}
}
