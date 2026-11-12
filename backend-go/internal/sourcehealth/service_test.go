package sourcehealth

import (
	"context"
	"strings"
	"testing"
	"time"
)

type repositoryFixture struct {
	snapshot Snapshot
	hasRun   bool
	since    time.Time
	before   time.Time
	reads    int
}

func (r *repositoryFixture) Weekly(_ context.Context, since, before time.Time) (Snapshot, error) {
	r.since, r.before = since, before
	r.reads++
	return r.snapshot, nil
}

func (r *repositoryFixture) HasRun(_ context.Context, since, before time.Time) (bool, error) {
	r.since, r.before = since, before
	return r.hasRun, nil
}

type messengerFixture struct {
	chat  string
	text  string
	calls int
}

func (m *messengerFixture) SendText(_ context.Context, chat, text string) error {
	m.chat, m.text = chat, text
	m.calls++
	return nil
}

func TestSourceHealthDueUsesBeijingMondayAndMissedOnceWindow(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		at       time.Time
		wantWeek string
		wantDue  bool
	}{
		{name: "before monday schedule", at: time.Date(2026, 10, 12, 8, 59, 0, 0, location), wantDue: false},
		{name: "scheduled monday", at: time.Date(2026, 10, 12, 9, 0, 0, 0, location), wantWeek: "2026-10-12", wantDue: true},
		{name: "missed monday runs once later in the same week", at: time.Date(2026, 10, 14, 13, 0, 0, 0, location), wantWeek: "2026-10-12", wantDue: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &repositoryFixture{}
			service := NewService(repo, nil, Config{})
			week, due, err := service.Due(context.Background(), test.at)
			if err != nil || week != test.wantWeek || due != test.wantDue {
				t.Fatalf("Due = %q, %t, %v; want %q, %t", week, due, err, test.wantWeek, test.wantDue)
			}
			if test.wantDue {
				if !repo.since.Equal(time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)) || !repo.before.Equal(repo.since.AddDate(0, 0, 7)) {
					t.Fatalf("missed-run window = %s..%s", repo.since, repo.before)
				}
			} else if !repo.since.IsZero() {
				t.Fatal("the future Monday slot must not be treated as missed")
			}
		})
	}
	repo := &repositoryFixture{hasRun: true}
	week, due, err := NewService(repo, nil, Config{}).Due(context.Background(), time.Date(2026, 10, 14, 13, 0, 0, 0, location))
	if err != nil || due || week != "" {
		t.Fatalf("completed weekly report was not suppressed: week=%q due=%t err=%v", week, due, err)
	}
}

func TestWeeklyReportKeepsEverySourceGroupAndUsesFeishuSafetyValve(t *testing.T) {
	now := time.Date(2026, 10, 14, 13, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	old := now.Add(-8 * 24 * time.Hour)
	repo := &repositoryFixture{snapshot: Snapshot{
		Enabled: 21, Failing: 2, Degraded: 1, Added: 1, ThisWeek: 1_234, LastWeek: 1_000, Selected: 27,
		Sources: []Source{
			{ID: "editorial-failing", Name: "编辑源", Mode: "editorial", CreatedAt: old, Health: "failing", FailCount: 7, LastError: stringPtr("HTTP 503"), Runs: 20, Failed: 5, Items: 3, Undated: 1, Repeated: 1, DetailFailures: 2},
			{ID: "editorial-new", Name: "新源", Mode: "editorial", CreatedAt: now.Add(-24 * time.Hour), Runs: 2, Discoveries: 0},
			{ID: "signal", Name: "信号源", Mode: "hot_signal", CreatedAt: old, Runs: 4, Discoveries: 1, Items: 5},
		},
	}}
	messenger := &messengerFixture{}
	service := NewService(repo, messenger, Config{Enabled: true, InternalChatID: "internal-chat", EnvironmentName: "staging"})
	service.now = func() time.Time { return now }
	result, err := service.Run(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failing != 2 || result.Silent != 1 || repo.reads != 1 {
		t.Fatalf("result=%#v reads=%d", result, repo.reads)
	}
	if !repo.since.Equal(now.UTC().Add(-7*24*time.Hour)) || !repo.before.Equal(now.UTC().Add(-14*24*time.Hour)) {
		t.Fatalf("query windows = %s..%s", repo.since, repo.before)
	}
	if messenger.calls != 1 || messenger.chat != "internal-chat" {
		t.Fatalf("message was not sent through the fallback chat: %#v", messenger)
	}
	for _, expected := range []string{"[staging] 📊 信源周报", "本周收录 1,234 条", "编辑内容", "热度信号", "editorial-failing", "5/20", "缺发布时间 1 篇", "详情补全失败"} {
		if !strings.Contains(messenger.text, expected) {
			t.Errorf("weekly report omitted %q: %s", expected, messenger.text)
		}
	}
}

func TestDisabledWeeklyReportNeverCallsMessenger(t *testing.T) {
	repo := &repositoryFixture{snapshot: Snapshot{Failing: 3}}
	messenger := &messengerFixture{}
	result, err := NewService(repo, messenger, Config{Enabled: false, AlertChatID: "alert"}).Run(context.Background(), time.Now())
	if err != nil || result.Failing != 3 || messenger.calls != 0 {
		t.Fatalf("result=%#v calls=%d err=%v", result, messenger.calls, err)
	}
}

func TestReportPercentAndFormatting(t *testing.T) {
	if got := percent(12, 10); got != "+20%" {
		t.Fatalf("percent = %s", got)
	}
	if got := percent(8, 10); got != "-20%" {
		t.Fatalf("percent = %s", got)
	}
	if got := percent(0, 0); got != "—" {
		t.Fatalf("zero-baseline percent = %s", got)
	}
	if got := comma(1234567); got != "1,234,567" {
		t.Fatalf("comma = %s", got)
	}
}

func stringPtr(value string) *string { return &value }
