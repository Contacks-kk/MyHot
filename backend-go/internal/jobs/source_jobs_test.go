package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"aihot/backend-go/internal/collector"
)

func TestManualSourceFetchAddsOnlyTheExplicitForceArgument(t *testing.T) {
	if got := (SourceArgs{}).Kind(); got != "myhot_collect_source" {
		t.Fatalf("source job kind changed: %q", got)
	}
	for _, test := range []struct {
		args SourceArgs
		want string
	}{
		{args: SourceArgs{SourceID: "rss-demo"}, want: `{"sourceId":"rss-demo"}`},
		{args: SourceArgs{SourceID: "rss-demo", Force: true}, want: `{"sourceId":"rss-demo","force":true}`},
	} {
		encoded, err := json.Marshal(test.args)
		if err != nil || string(encoded) != test.want {
			t.Fatalf("source args = %s, err=%v; want %s", encoded, err, test.want)
		}
	}
}

func TestManualSourceFetchUsesExistingKindsAndWorkerOptions(t *testing.T) {
	for _, test := range []struct {
		kind     string
		wantKind string
		queue    string
		max      int
	}{
		{kind: "rss", wantKind: "myhot_collect_source", queue: "rss_sources", max: 1},
		{kind: "web_list", wantKind: "myhot_collect_source", queue: "rss_sources", max: 1},
		{kind: "json_list", wantKind: "myhot_collect_source", queue: "rss_sources", max: 1},
		{kind: "x_search", wantKind: "myhot_collect_source", queue: "rss_sources", max: 1},
		{kind: "mp_account", wantKind: "myhot_mp_account_check", queue: "mp_accounts", max: 4},
	} {
		args, opts, err := ManualSourceFetchArgs("manual-demo", test.kind)
		if err != nil || args.Kind() != test.wantKind || opts.Queue != test.queue || opts.MaxAttempts != test.max {
			t.Fatalf("manual %s job = args=%#v opts=%#v err=%v", test.kind, args, opts, err)
		}
		encoded, err := json.Marshal(args)
		if err != nil || !strings.Contains(string(encoded), `"sourceId":"manual-demo"`) {
			t.Fatalf("manual %s args payload = %s, err=%v", test.kind, encoded, err)
		}
	}
	if args, opts, err := ManualSourceFetchArgs("manual-demo", "external"); err == nil || args != nil || opts != nil {
		t.Fatalf("unsupported manual source kind was accepted: args=%#v opts=%#v err=%v", args, opts, err)
	}
}

func TestMPAccountQueuePayloadRemainsSourceScoped(t *testing.T) {
	args := MPCheckArgs{SourceID: "wechat-1"}
	if got := args.Kind(); got != "myhot_mp_account_check" {
		t.Fatalf("MP worker kind = %q", got)
	}
	encoded, err := json.Marshal(args)
	if err != nil || string(encoded) != `{"sourceId":"wechat-1"}` {
		t.Fatalf("MP job args = %s, err=%v", encoded, err)
	}
}

func TestRecordedSourceFailureDoesNotRetryInRiver(t *testing.T) {
	if err := finishSourceWork(fmt.Errorf("collect source: %w", collector.ErrFetchRecorded)); err != nil {
		t.Fatalf("recorded source failure should complete the River job: %v", err)
	}
	infrastructure := errors.New("database unavailable")
	if err := finishSourceWork(infrastructure); !errors.Is(err, infrastructure) {
		t.Fatalf("unrecorded failure should stay retryable: %v", err)
	}
}
