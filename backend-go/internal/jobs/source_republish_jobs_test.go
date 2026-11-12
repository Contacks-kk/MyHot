package jobs

import (
	"encoding/json"
	"testing"
)

func TestSourceRepublishArgsAndRetryContract(t *testing.T) {
	args := SourceRepublishArgs{SourceID: "rss-demo"}
	if args.Kind() != "myhot_republish_source" {
		t.Fatalf("source republish kind = %q", args.Kind())
	}
	encoded, err := json.Marshal(args)
	if err != nil || string(encoded) != `{"sourceId":"rss-demo"}` {
		t.Fatalf("source republish args = %s err=%v", encoded, err)
	}
	opts := SourceRepublishInsertOpts()
	if opts.Queue != "source_republish" || opts.Priority != 2 || opts.MaxAttempts != 3 || !opts.UniqueOpts.ByArgs {
		t.Fatalf("source republish retry/uniqueness contract = %#v", opts)
	}
	if got := (SourceRepublishScanArgs{}).Kind(); got != "myhot_republish_source_scan" {
		t.Fatalf("source republish scan kind = %q", got)
	}
}
