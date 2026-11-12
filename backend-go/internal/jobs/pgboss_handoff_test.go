package jobs

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPgBossRiverJobTransfersManualSourceFetchAndRetiresScheduledWork(t *testing.T) {
	job := pgBossPendingJob{Name: "sources.fetch", SourceKind: "web_list", Data: []byte(`{"sourceId":"industry-watch","force":true}`)}
	args, opts, transfer, err := pgBossRiverJob(job)
	if err != nil || !transfer {
		t.Fatalf("manual fetch transfer = %v, %v", transfer, err)
	}
	if got := args.(SourceArgs); got.SourceID != "industry-watch" || !got.Force {
		t.Fatalf("manual fetch args = %#v", got)
	}
	if opts.MaxAttempts != 1 {
		t.Fatalf("manual fetch attempts = %d", opts.MaxAttempts)
	}

	job.Data = []byte(`{"sourceId":"industry-watch"}`)
	_, _, transfer, err = pgBossRiverJob(job)
	if err != nil || transfer {
		t.Fatalf("scheduled fetch should be reconstructed by Go scanner: transfer=%v err=%v", transfer, err)
	}
}

func TestPgBossRiverJobPreservesManualMPAndNotificationAttempts(t *testing.T) {
	mp := pgBossPendingJob{Name: "sources.mp", RetryCount: 1, Data: []byte(`{"sourceId":"mp-source","reason":"manual"}`)}
	args, opts, transfer, err := pgBossRiverJob(mp)
	if err != nil || !transfer {
		t.Fatalf("manual MP transfer = %v, %v", transfer, err)
	}
	if got := args.(MPCheckArgs); got.SourceID != "mp-source" || opts.Queue != "mp_accounts" || opts.MaxAttempts != 3 {
		t.Fatalf("manual MP transfer = %#v %#v", args, opts)
	}

	notify := pgBossPendingJob{Name: "notify.selected", StartAfter: time.Now().Add(3 * time.Minute), Data: []byte(`{"articleId":"article-1","attempt":4}`)}
	args, opts, transfer, err = pgBossRiverJob(notify)
	if err != nil || !transfer {
		t.Fatalf("notification transfer = %v, %v", transfer, err)
	}
	if got := args.(SelectedNotifyArgs); got.ArticleID != "article-1" || got.Attempt != 4 || opts.MaxAttempts != 1 || opts.Queue != "content_delivery" || !opts.ScheduledAt.Equal(notify.StartAfter) {
		t.Fatalf("notification transfer = %#v %#v", args, opts)
	}
}

func TestPgBossRiverJobTransfersQueuedSourceRepublish(t *testing.T) {
	job := pgBossPendingJob{Name: "publication.republish-source", RetryCount: 1, StartAfter: time.Now().Add(time.Minute), Data: []byte(`{"sourceId":"industry-watch"}`)}
	args, opts, transfer, err := pgBossRiverJob(job)
	if err != nil || !transfer {
		t.Fatalf("republish transfer = %v, %v", transfer, err)
	}
	if got := args.(SourceRepublishArgs); got.SourceID != "industry-watch" || opts.Queue != "source_republish" || opts.MaxAttempts != 2 || !opts.ScheduledAt.Equal(job.StartAfter) {
		t.Fatalf("republish transfer = %#v %#v", args, opts)
	}
}

func TestPgBossRiverJobTransfersBothMediaPayloadsWithRemainingRetries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  string
		check func(*testing.T, any, int)
	}{
		{"article", `{"articleId":"article-1"}`, func(t *testing.T, args any, attempts int) {
			if got := args.(MediaPrepareArgs); got.ArticleID != "article-1" || attempts != 2 {
				t.Fatalf("article media = %#v, attempts=%d", got, attempts)
			}
		}},
		{"url", `{"url":"https://images.example.invalid/a.gif","mode":"article"}`, func(t *testing.T, args any, attempts int) {
			if got := args.(MediaPrepareArgs); got.URL != "https://images.example.invalid/a.gif" || got.Mode != "article" || attempts != 1 {
				t.Fatalf("URL media = %#v, attempts=%d", got, attempts)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, opts, transfer, err := pgBossRiverJob(pgBossPendingJob{Name: "media.prepare", Data: json.RawMessage(tc.data), RetryCount: map[string]int{"article": 0, "url": 1}[tc.name]})
			if err != nil || !transfer {
				t.Fatalf("media transfer = %v, %v", transfer, err)
			}
			tc.check(t, args, opts.MaxAttempts)
		})
	}
}

func TestPgBossRiverJobRejectsMalformedOwnedMessages(t *testing.T) {
	for _, job := range []pgBossPendingJob{
		{Name: "notify.selected", Data: []byte(`{"attempt":1}`)},
		{Name: "media.prepare", Data: []byte(`{"url":"https://example.invalid/a.gif"}`)},
		{Name: "sources.mp", Data: []byte(`{"sourceId":"mp-source","reason":2}`)},
	} {
		if _, _, _, err := pgBossRiverJob(job); err == nil {
			t.Fatalf("malformed %s message was accepted: %s", job.Name, job.Data)
		}
	}
}
