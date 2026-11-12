package jobs

import (
	"testing"
	"time"
)

func TestBeijingScheduleNextEight(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule := beijingSchedule{location: loc}
	before := time.Date(2026, 10, 4, 7, 30, 0, 0, loc)
	if got := schedule.Next(before); !got.Equal(time.Date(2026, 10, 4, 8, 0, 0, 0, loc)) {
		t.Fatalf("next = %s", got)
	}
	after := time.Date(2026, 10, 4, 8, 0, 0, 1, loc)
	if got := schedule.Next(after); !got.Equal(time.Date(2026, 10, 5, 8, 0, 0, 0, loc)) {
		t.Fatalf("next = %s", got)
	}
}

func TestDailyScanStartupDoesNotSuppressEightAMRun(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 10, 4, 7, 59, 0, 0, loc)
	next := beijingSchedule{location: loc}.Next(before)
	period := dailyScanInsertOpts().UniqueOpts.ByPeriod
	if before.Truncate(period).Equal(next.Truncate(period)) {
		t.Fatalf("RunOnStart uniqueness period %s suppresses the 08:00 scan", period)
	}
}

func TestMPAccountScanRunsAtQuarterHourBoundariesAndSkipsMissedSlots(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule := mpAccountSchedule{location: loc}
	for _, test := range []struct{ current, want time.Time }{
		{time.Date(2026, 10, 8, 14, 0, 1, 0, loc), time.Date(2026, 10, 8, 14, 15, 0, 0, loc)},
		{time.Date(2026, 10, 8, 14, 15, 0, 0, loc), time.Date(2026, 10, 8, 14, 30, 0, 0, loc)},
		{time.Date(2026, 10, 8, 14, 59, 59, 0, loc), time.Date(2026, 10, 8, 15, 0, 0, 0, loc)},
	} {
		if got := schedule.Next(test.current); !got.Equal(test.want) {
			t.Fatalf("next after %s = %s, want %s", test.current, got, test.want)
		}
	}
	if mpAccountPeriodicOptions().RunOnStart {
		t.Fatal("the old cron skips missed slots instead of running immediately at startup")
	}
}

func TestIndexNowRunsAtBeijingFiveFiftyAndSkipsMissedSlots(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := newIndexNowSchedule()
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 10, 8, 5, 49, 0, 0, loc)
	if got, want := schedule.Next(before), time.Date(2026, 10, 8, 5, 50, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	after := time.Date(2026, 10, 8, 5, 50, 1, 0, loc)
	if got, want := schedule.Next(after), time.Date(2026, 10, 9, 5, 50, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("next after missed slot = %s, want %s", got, want)
	}
	if !indexNowPeriodicOptions().RunOnStart {
		t.Fatal("startup catch-up must preserve the old missed-once behavior")
	}
}

func TestStoryLinksRunsAtBeijingMinuteTwelveAndSkipsMissedSlots(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule := storyLinksSchedule{location: loc}
	before := time.Date(2026, 10, 8, 14, 11, 0, 0, loc)
	if got, want := schedule.Next(before), time.Date(2026, 10, 8, 14, 12, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	after := time.Date(2026, 10, 8, 14, 12, 1, 0, loc)
	if got, want := schedule.Next(after), time.Date(2026, 10, 8, 15, 12, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("next after missed slot = %s, want %s", got, want)
	}
	if storyLinksPeriodicOptions().RunOnStart {
		t.Fatal("the old cron skips missed slots instead of running immediately at startup")
	}
}

func TestHeatSnapshotRunsAtBeijingMinuteTwoAndSkipsMissedSlots(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := newHeatSnapshotSchedule()
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 10, 8, 14, 1, 0, 0, loc)
	if got, want := schedule.Next(before), time.Date(2026, 10, 8, 14, 2, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	after := time.Date(2026, 10, 8, 14, 2, 1, 0, loc)
	if got, want := schedule.Next(after), time.Date(2026, 10, 8, 15, 2, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("next after missed slot = %s, want %s", got, want)
	}
	if heatSnapshotPeriodicOptions().RunOnStart {
		t.Fatal("the old cron skips missed slots instead of running immediately at startup")
	}
}

func TestHotRankingRunsOnFiveMinuteBoundariesAndSkipsMissedSlots(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := newHotRankingSchedule()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		current time.Time
		want    time.Time
	}{
		{time.Date(2026, 10, 8, 14, 2, 30, 0, loc), time.Date(2026, 10, 8, 14, 5, 0, 0, loc)},
		{time.Date(2026, 10, 8, 14, 5, 1, 0, loc), time.Date(2026, 10, 8, 14, 10, 0, 0, loc)},
	} {
		if got := schedule.Next(test.current); !got.Equal(test.want) {
			t.Fatalf("next after %s = %s, want %s", test.current, got, test.want)
		}
	}
	if hotRankingPeriodicOptions().RunOnStart {
		t.Fatal("the old cron skips missed slots instead of running immediately at startup")
	}
}

func TestTranslationRunsOnFiveMinuteBoundariesAndSkipsMissedSlots(t *testing.T) {
	for _, test := range []struct {
		current time.Time
		want    time.Time
	}{
		{time.Date(2026, 10, 8, 14, 2, 30, 0, time.UTC), time.Date(2026, 10, 8, 14, 5, 0, 0, time.UTC)},
		{time.Date(2026, 10, 8, 14, 5, 1, 0, time.UTC), time.Date(2026, 10, 8, 14, 10, 0, 0, time.UTC)},
	} {
		if got := (fiveMinuteSchedule{}).Next(test.current); !got.Equal(test.want) {
			t.Fatalf("next after %s = %s, want %s", test.current, got, test.want)
		}
	}
	opts := translationInsertOpts()
	if opts.Queue != "translations" || opts.MaxAttempts != 2 || opts.UniqueOpts.ByPeriod != 5*time.Minute {
		t.Fatalf("translation insert options = %#v", opts)
	}
	if got := (&TranslationScanWorker{}).Timeout(nil); got != 7*time.Minute {
		t.Fatalf("worker timeout = %s, want 7m", got)
	}
}

func TestReportComposeRunsOnBeijingHalfHoursAndCatchesUpOnStart(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule := halfHourSchedule{location: loc}
	for _, test := range []struct{ current, want time.Time }{
		{time.Date(2026, 10, 8, 14, 2, 30, 0, loc), time.Date(2026, 10, 8, 14, 30, 0, 0, loc)},
		{time.Date(2026, 10, 8, 14, 30, 0, 0, loc), time.Date(2026, 10, 8, 15, 0, 0, 0, loc)},
		{time.Date(2026, 10, 8, 14, 59, 59, 0, loc), time.Date(2026, 10, 8, 15, 0, 0, 0, loc)},
	} {
		if got := schedule.Next(test.current); !got.Equal(test.want) {
			t.Errorf("next after %s = %s, want %s", test.current, got, test.want)
		}
	}
	job := reportComposePeriodicJob(schedule)
	if job == nil {
		t.Fatal("report composition periodic job is nil")
	}
	options := reportComposeInsertOpts()
	if options.Queue != "reports" || options.MaxAttempts != 2 || options.UniqueOpts.ByPeriod != 30*time.Minute {
		t.Fatalf("report compose options = %#v", options)
	}
	if got := (&ReportComposeWorker{}).Timeout(nil); got != 30*time.Minute {
		t.Fatalf("report compose timeout = %s", got)
	}
}
