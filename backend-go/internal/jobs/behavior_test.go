package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/provider"
	"github.com/riverqueue/river"
)

func TestSelectedNotifyRiverArgsKeepPgBossPayloadShape(t *testing.T) {
	if kind := (SelectedNotifyArgs{}).Kind(); kind != "notify.selected" {
		t.Fatalf("selected notification kind = %q", kind)
	}
	first, err := json.Marshal(SelectedNotifyArgs{ArticleID: "article-1"})
	if err != nil || string(first) != `{"articleId":"article-1"}` {
		t.Fatalf("initial pg-boss payload shape = %s, err=%v", first, err)
	}
	retry, err := json.Marshal(SelectedNotifyArgs{ArticleID: "article-1", Attempt: 2})
	if err != nil || string(retry) != `{"articleId":"article-1","attempt":2}` {
		t.Fatalf("retry pg-boss payload shape = %s, err=%v", retry, err)
	}
}

func TestSelectedNotifyRiverRetriesOnlyTheExplicitSameTitleSchedule(t *testing.T) {
	when := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	opts := selectedNotifyInsertOpts(when)
	if opts.Queue != "content_delivery" || opts.MaxAttempts != 1 || !opts.ScheduledAt.Equal(when) {
		t.Fatalf("selected notification retry options = %#v", opts)
	}
	if !opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) != 5 {
		t.Fatalf("selected notification uniqueness = %#v", opts.UniqueOpts)
	}
}

func TestDailyWindowIsFixedInBeijing(t *testing.T) {
	start, end, err := dailyWindow("2026-10-04")
	if err != nil {
		t.Fatal(err)
	}
	// 08:00 Beijing is 00:00 UTC. Retries use the date instead of execution time.
	if got := start.UTC().Format(time.RFC3339); got != "2026-10-03T00:00:00Z" {
		t.Fatalf("start = %s", got)
	}
	if got := end.UTC().Format(time.RFC3339); got != "2026-10-04T00:00:00Z" {
		t.Fatalf("end = %s", got)
	}
	if end.Sub(start) != 24*time.Hour {
		t.Fatalf("window = %s", end.Sub(start))
	}
	if _, _, err := dailyWindow("2026-02-30"); err == nil {
		t.Fatal("invalid date was accepted")
	}
}

/** TestPausedAutomaticWorkersPreserveTasks checks disabled worker behavior. */
func TestPausedAutomaticWorkersPreserveTasks(t *testing.T) {
	// Nil DB intentionally ensures the disabled path cannot read inputs or call a model.
	service := NewService(nil, config.Config{}, nil)
	article := &ArticleWorker{Service: service, Enabled: false}
	daily := &DailyWorker{Service: service, Enabled: false}
	for _, err := range []error{article.Work(context.Background(), &river.Job[ArticleArgs]{}), daily.Work(context.Background(), &river.Job[DailyArgs]{})} {
		var snooze *river.JobSnoozeError
		if !errors.As(err, &snooze) || snooze.Duration != 5*time.Minute {
			t.Fatalf("paused task result = %v", err)
		}
	}
	if err := (&SourceWorker{Enabled: false}).Work(context.Background(), &river.Job[SourceArgs]{Args: SourceArgs{SourceID: "manual", Force: true}}); err != nil {
		var snooze *river.JobSnoozeError
		if !errors.As(err, &snooze) || snooze.Duration != 5*time.Minute {
			t.Fatalf("manual source task escaped the collection gate: %v", err)
		}
	}
	if err := (&ScanWorker{Enabled: false}).Work(context.Background(), &river.Job[ScanArgs]{}); err != nil {
		t.Fatal(err)
	}
	if err := (&TriageBatchWorker{Enabled: false}).Work(context.Background(), &river.Job[TriageBatchArgs]{}); err != nil {
		var snooze *river.JobSnoozeError
		if !errors.As(err, &snooze) || snooze.Duration != 5*time.Minute {
			t.Fatalf("disabled triage batch worker = %v", err)
		}
	} else {
		t.Fatal("disabled triage batch worker should remain queued")
	}
	if err := (&DailyScanWorker{Enabled: false}).Work(context.Background(), &river.Job[DailyScanArgs]{}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRetryClassificationCancelsPermanentFailure(t *testing.T) {
	permanent := &provider.Error{Message: "模型服务返回 HTTP 401"}
	transient := &provider.Error{Message: "模型服务返回 HTTP 429", Retryable: true}
	var cancelled *river.JobCancelError
	if !errors.As(callJobError(permanent), &cancelled) {
		t.Fatal("invalid credential should cancel the paid job")
	}
	if errors.As(callJobError(transient), &cancelled) {
		t.Fatal("rate limit should retain River retry backoff")
	}
}

/** TestStage1MatchesInterestsAndAllowsEmptyPreferences verifies Stage 1 zero-token filtering. */
func TestStage1MatchesInterestsAndAllowsEmptyPreferences(t *testing.T) {
	service := NewService(nil, config.Config{}, nil)
	if hits, ok := service.matchesUserInterests("任何新闻", "", "正文中没有关键词", nil, nil); !ok || len(hits) != 0 {
		t.Fatalf("empty preferences should pass into triage without hits: ok=%t hits=%v", ok, hits)
	}
	if _, ok := service.matchesUserInterests("Weather forecast", "", "Sunny day", []string{"OpenAI"}, nil); ok {
		t.Fatal("configured keyword filter failed to drop unrelated noise")
	}
	hits, ok := service.matchesUserInterests("OpenAI updates API", "", "", []string{"OpenAI"}, nil)
	if !ok || len(hits) != 1 {
		t.Fatalf("matching keyword result = ok=%t hits=%v", ok, hits)
	}
}

/** TestPlanUserDispatchesSplitsSmallDirectVsBatchTriageAndRespectsQuota verifies Stage 2 routing and caps. */
func TestPlanUserDispatchesSplitsSmallDirectVsBatchTriageAndRespectsQuota(t *testing.T) {
	makeItems := func(n int) []userScanCandidate {
		items := make([]userScanCandidate, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, userScanCandidate{
				articleID:         "art_" + string(rune('a'+i%26)) + "_" + time.Duration(i).String(),
				revision:          1,
				sourceID:          "src_1",
				title:             "Title",
				body:              "Body content",
				modelVersion:      1,
				preferenceVersion: 2,
				coldStart:         i%2 == 0,
			})
		}
		return items
	}

	articles, triages := planUserDispatches(10, makeItems(5), 50, true)
	if len(articles) != 5 || len(triages) != 0 {
		t.Fatalf("K=5 dispatch = %d articles / %d triages, want 5/0", len(articles), len(triages))
	}

	articles, triages = planUserDispatches(10, makeItems(12), 50, true)
	if len(articles) != 0 || len(triages) != 1 || len(triages[0].Items) != 12 {
		t.Fatalf("K=12 dispatch = %d articles / %d triages, want 0/1(12)", len(articles), len(triages))
	}

	articles, triages = planUserDispatches(10, makeItems(28), 50, true)
	if len(triages) != 1 || len(triages[0].Items) != 25 || len(articles) != 3 {
		t.Fatalf("K=28 dispatch = %d articles / %d triages, want 3 direct + 1 batch(25)", len(articles), len(triages))
	}

	articles, triages = planUserDispatches(10, makeItems(10), 1, true)
	if len(articles) != 1 || len(triages) != 0 {
		t.Fatalf("remaining=1 dispatch = %d articles / %d triages, want 1/0", len(articles), len(triages))
	}
}

/** TestParseTriageSelectionHandlesMarkdownFencesDuplicatesAndOutOfRange checks JSON selection parsing. */
func TestParseTriageSelectionHandlesMarkdownFencesDuplicatesAndOutOfRange(t *testing.T) {
	got, err := parseTriageSelection("```json\n{\"selected\":[3, 1, 3, 99, 2, 4, 5, 6]}\n```", 8, 5)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	want := []int{3, 1, 2, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("picked = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("picked[%d] = %d, want %d", i, got[i], want[i])
		}
	}
	empty, err := parseTriageSelection(`{"selected":[]}`, 5, 5)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty valid selection = %v, %v", empty, err)
	}
	if _, err := parseTriageSelection(`{}`, 5, 5); err == nil {
		t.Fatal("missing selected array should fail")
	}
	if _, err := parseTriageSelection(`{"selected":[99]}`, 5, 5); err == nil {
		t.Fatal("out-of-range-only selection should fail")
	}
}

/** TestCompactSnippetAndShanghaiDayStart checks snippet normalization and daily window alignment. */
func TestCompactSnippetAndShanghaiDayStart(t *testing.T) {
	if got := compactSnippet("  line1 \n\t  line2  ", "fallback", 10); got != "line1 line…" {
		t.Fatalf("compact snippet = %q", got)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	probe := time.Date(2026, 11, 11, 2, 30, 0, 0, loc)
	dayStart, err := shanghaiDayStartUTC(probe)
	if err != nil {
		t.Fatal(err)
	}
	if got := dayStart.Format(time.RFC3339); got != "2026-11-10T16:00:00Z" {
		t.Fatalf("day start UTC = %s", got)
	}
}
