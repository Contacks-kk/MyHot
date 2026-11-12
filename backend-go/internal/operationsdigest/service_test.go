package operationsdigest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/sourcehealth"
)

type repositoryFixture struct {
	snapshot Snapshot
	run      bool
	since    time.Time
	before   time.Time
	err      error
}

func (r *repositoryFixture) Digest(context.Context, time.Time) (Snapshot, error) {
	return r.snapshot, r.err
}
func (r *repositoryFixture) HasRun(_ context.Context, since, before time.Time) (bool, error) {
	r.since, r.before = since, before
	return r.run, r.err
}

type messengerFixture struct {
	chat  string
	text  string
	err   error
	calls int
}

func (m *messengerFixture) SendText(_ context.Context, chat, text string) error {
	m.chat, m.text, m.calls = chat, text, m.calls+1
	return m.err
}

func TestDueUsesBeijingTimeAndRunsMissedDigestOnce(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	repo := &repositoryFixture{}
	service := NewService(repo, nil, Config{})

	date, due, err := service.Due(context.Background(), time.Date(2026, 10, 8, 8, 59, 0, 0, location))
	if err != nil || due || date != "" {
		t.Fatalf("before 09:00: date=%q due=%v err=%v", date, due, err)
	}
	date, due, err = service.Due(context.Background(), time.Date(2026, 10, 8, 15, 30, 0, 0, location))
	if err != nil || !due || date != "2026-10-08" {
		t.Fatalf("missed slot should run once: date=%q due=%v err=%v", date, due, err)
	}
	if !repo.since.Equal(time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)) || !repo.before.Equal(time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected UTC day range: %s - %s", repo.since, repo.before)
	}
	repo.run = true
	if _, due, err := service.Due(context.Background(), time.Date(2026, 10, 8, 16, 0, 0, 0, location)); err != nil || due {
		t.Fatalf("already-run digest should not be due: due=%v err=%v", due, err)
	}
}

func TestRunFormatsDigestAndUsesSafetyValveFallback(t *testing.T) {
	services := "llm/article、llm/translation"
	repo := &repositoryFixture{snapshot: Snapshot{
		UnknownReceipts:   2,
		UnknownServices:   &services,
		UnknownDeliveries: 1,
		Queues:            []Queue{{Name: "content.analyze", Count: 3, Oldest: time.Date(2026, 10, 7, 20, 0, 0, 0, time.UTC)}},
	}}
	messenger := &messengerFixture{}
	service := NewService(repo, messenger, Config{
		FeishuInternalEnabled: true,
		InternalChatID:        "internal-chat",
		EnvironmentName:       "staging",
	})
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	result, err := service.Run(context.Background(), now)
	if err != nil || result.Items != 3 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if messenger.calls != 1 || messenger.chat != "internal-chat" {
		t.Fatalf("message calls=%d chat=%q", messenger.calls, messenger.chat)
	}
	for _, part := range []string{"【staging】📋 系统日报 · 10月8日", "2 个付费请求的结果尚未确认", services, "后台任务排队超过 2 小时：content.analyze"} {
		if !strings.Contains(messenger.text, part) {
			t.Errorf("digest missing %q in:\n%s", part, messenger.text)
		}
	}
}

func TestRunRespectsDisabledValveAndEmptyDigest(t *testing.T) {
	unknown := &repositoryFixture{snapshot: Snapshot{UnknownReceipts: 1}}
	messenger := &messengerFixture{}
	service := NewService(unknown, messenger, Config{AlertChatID: "alert-chat"})
	result, err := service.Run(context.Background(), time.Now())
	if err != nil || result.Items != 1 || messenger.calls != 0 {
		t.Fatalf("disabled valve should retain audit result without sending: %+v calls=%d err=%v", result, messenger.calls, err)
	}
	empty := &repositoryFixture{}
	service = NewService(empty, messenger, Config{FeishuInternalEnabled: true, AlertChatID: "alert-chat"})
	result, err = service.Run(context.Background(), time.Now())
	if err != nil || result.Items != 0 || messenger.calls != 0 {
		t.Fatalf("empty digest should not send: %+v calls=%d err=%v", result, messenger.calls, err)
	}
}

func TestRunReturnsRepositoryAndMessengerErrors(t *testing.T) {
	want := errors.New("read failed")
	service := NewService(&repositoryFixture{err: want}, nil, Config{})
	if _, err := service.Run(context.Background(), time.Now()); !errors.Is(err, want) {
		t.Fatalf("repository error = %v", err)
	}
	messenger := &messengerFixture{err: want}
	service = NewService(&repositoryFixture{snapshot: Snapshot{UnknownDeliveries: 1}}, messenger, Config{FeishuInternalEnabled: true, AlertChatID: "chat"})
	if _, err := service.Run(context.Background(), time.Now()); !errors.Is(err, want) {
		t.Fatalf("messenger error = %v", err)
	}
}

func TestSourceFindingsIncludesOnlyRelevantModesAndSourceWindow(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	errorMessage := "timeout"
	data := Snapshot{SourceHealthSince: since, SourceHealth: sourcehealth.Snapshot{Sources: []sourcehealth.Source{
		{ID: "old", Name: "Old", Mode: "editorial", CreatedAt: since.Add(-time.Hour), Discoveries: 0, Health: "failing", FailCount: 3, LastError: &errorMessage},
		{ID: "new", Name: "New", Mode: "editorial", CreatedAt: since.Add(time.Hour), Discoveries: 0},
		{ID: "ignored", Name: "Ignored", Mode: "external", CreatedAt: since.Add(-time.Hour), Health: "failing"},
	}}}
	items := sourceFindings(data)
	if len(items) != 2 || !strings.Contains(items[0].detail, "Old（old）") || !strings.Contains(items[1].title, "7 天未发现") {
		t.Fatalf("unexpected source findings: %+v", items)
	}
}

func TestBackupAndImageBudgetFollowDigestThresholds(t *testing.T) {
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	backupAt := now.Add(-31 * time.Hour)
	snapshot := Snapshot{
		Backup:             &BackupState{At: backupAt.Format(time.RFC3339Nano), Uploaded: false},
		FirstBackupAttempt: &backupAt,
		ImageBudget:        &ImageBudget{Window: "day", Reason: "budget full", UpdatedAt: now.Add(-time.Hour)},
	}
	service := NewService(&repositoryFixture{}, nil, Config{BackupConfigured: true})
	items := service.findings(snapshot, now)
	if len(items) != 2 || items[0].title != "数据库备份超过一天没成功" || !strings.Contains(items[0].detail, "最近一次 10月7日") || !strings.Contains(items[0].detail, "（未上传）") {
		t.Fatalf("stale backup finding = %+v", items)
	}
	if items[1].title != "图片代理今天的流量额度用完了，未缓存的图片到零点前显示不出来" {
		t.Fatalf("daily image budget finding = %+v", items[1])
	}
	snapshot.Backup.At = now.Add(-51 * time.Hour).Format(time.RFC3339Nano)
	firstAttempt := now.Add(-51 * time.Hour)
	snapshot.FirstBackupAttempt = &firstAttempt
	items = service.findings(snapshot, now)
	if len(items) != 1 || items[0].title != "图片代理今天的流量额度用完了，未缓存的图片到零点前显示不出来" {
		t.Fatalf("backup beyond two days belongs to urgent alerts, not digest: %+v", items)
	}
	snapshot.ImageBudget.UpdatedAt = now.Add(-25 * time.Hour)
	if items := service.findings(snapshot, now); len(items) != 0 {
		t.Fatalf("expired image budget should not be reported: %+v", items)
	}
}
