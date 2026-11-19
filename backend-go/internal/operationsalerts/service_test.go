package operationsalerts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/sourcehealth"
)

type repositoryFixture struct {
	findings []Finding
	state    State
	err      error
	checks   int
	saves    int
}

func (r *repositoryFixture) Findings(context.Context, time.Time, Config) ([]Finding, error) {
	r.checks++
	return r.findings, r.err
}
func (r *repositoryFixture) LoadState(context.Context) (State, error) {
	if r.err != nil {
		return nil, r.err
	}
	copy := State{}
	for key, value := range r.state {
		copy[key] = value
	}
	return copy, nil
}
func (r *repositoryFixture) SaveState(_ context.Context, state State) error {
	r.saves++
	r.state = state
	return r.err
}

type messengerFixture struct {
	chat  string
	texts []string
	err   error
}

type workerWatchdogRepositoryFixture struct {
	heartbeatAt *time.Time
	state       *WorkerWatchdogState
	err         error
	saves       int
}

/** Runs a watchdog state change while the fixture represents the advisory lock. */
func (r *workerWatchdogRepositoryFixture) WithWorkerWatchdogLock(ctx context.Context, check func(WorkerWatchdogStateRepository) error) error {
	if r.err != nil {
		return r.err
	}
	return check(r)
}

/** Returns the fake Worker heartbeat timestamp. */
func (r *workerWatchdogRepositoryFixture) WorkerHeartbeatUpdatedAt(context.Context) (*time.Time, error) {
	return r.heartbeatAt, r.err
}

/** Returns a copy of the fake persisted watchdog state. */
func (r *workerWatchdogRepositoryFixture) LoadWorkerWatchdogState(context.Context) (*WorkerWatchdogState, error) {
	if r.state == nil {
		return nil, r.err
	}
	state := *r.state
	return &state, r.err
}

/** Records the fake persisted watchdog state. */
func (r *workerWatchdogRepositoryFixture) SaveWorkerWatchdogState(_ context.Context, state WorkerWatchdogState) error {
	r.saves++
	copy := state
	r.state = &copy
	return r.err
}

func (m *messengerFixture) SendText(_ context.Context, chat, text string) error {
	m.chat = chat
	m.texts = append(m.texts, text)
	return m.err
}

func TestCheckThrottlesRepeatsAndReportsRecovery(t *testing.T) {
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	started := now.Add(-2 * time.Hour)
	repo := &repositoryFixture{findings: []Finding{{
		Key: "content.collect", Level: LevelNow, Title: "网站停止收录新文章", Impact: "读者暂时看不到新内容", Heals: "没有", Action: "转给 AI", Detail: "inspect worker", Since: &started,
	}}}
	messenger := &messengerFixture{}
	service := NewService(repo, messenger, Config{FeishuInternalEnabled: true, AlertChatID: "alerts", InternalChatID: "internal", EnvironmentName: "staging"})

	first, err := service.Check(context.Background(), now)
	if err != nil || len(first.Open) != 1 || len(first.Sent) != 1 || messenger.chat != "alerts" || len(messenger.texts) != 1 {
		t.Fatalf("first check = %+v, chat=%q messages=%d err=%v", first, messenger.chat, len(messenger.texts), err)
	}
	for _, want := range []string{"【staging】🔴 网站停止收录新文章（已持续 2 小时）", "影响：读者暂时看不到新内容", "给 AI 的细节：inspect worker"} {
		if !strings.Contains(messenger.texts[0], want) {
			t.Errorf("alert missing %q: %s", want, messenger.texts[0])
		}
	}

	repo.findings = nil
	recovered, err := service.Check(context.Background(), now.Add(30*time.Minute))
	if err != nil || len(recovered.Open) != 0 || len(recovered.Sent) != 1 || recovered.Sent[0] != "content.collect:recovered" || len(messenger.texts) != 2 {
		t.Fatalf("recovery = %+v messages=%d err=%v", recovered, len(messenger.texts), err)
	}
	if !strings.Contains(messenger.texts[1], "✅ 已恢复：网站停止收录新文章") || !strings.Contains(messenger.texts[1], "持续 2 小时 30 分钟（10月8日 08:00 起）") {
		t.Fatalf("recovery message = %s", messenger.texts[1])
	}
}

func TestCheckRepeatIntervalsAndSafetyValve(t *testing.T) {
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	repo := &repositoryFixture{findings: []Finding{{Key: "quota", Level: LevelToday, Title: "额度用完", Since: &now}}}
	messenger := &messengerFixture{}
	service := NewService(repo, messenger, Config{FeishuInternalEnabled: true, InternalChatID: "internal"})
	if _, err := service.Check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Check(context.Background(), now.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(messenger.texts) != 1 {
		t.Fatalf("today alert repeated too soon: %d", len(messenger.texts))
	}
	if _, err := service.Check(context.Background(), now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(messenger.texts) != 2 || !strings.Contains(messenger.texts[1], "仍未恢复") {
		t.Fatalf("daily repeat missing: %v", messenger.texts)
	}

	disabled := &repositoryFixture{findings: repo.findings}
	service = NewService(disabled, messenger, Config{})
	if result, err := service.Check(context.Background(), now); err != nil || len(result.Open) != 1 || disabled.saves != 1 || len(messenger.texts) != 2 {
		t.Fatalf("disabled delivery should still record alert state: result=%+v saves=%d messages=%d err=%v", result, disabled.saves, len(messenger.texts), err)
	}
}

func TestCheckReturnsRepositoryAndMessengerErrors(t *testing.T) {
	want := errors.New("repository failed")
	service := NewService(&repositoryFixture{err: want}, nil, Config{})
	if _, err := service.Check(context.Background(), time.Now()); !errors.Is(err, want) {
		t.Fatalf("repository error = %v", err)
	}
	repository := &repositoryFixture{findings: []Finding{{Key: "x", Level: LevelNow, Title: "x"}}}
	messenger := &messengerFixture{err: want}
	service = NewService(repository, messenger, Config{FeishuInternalEnabled: true, AlertChatID: "alerts"})
	if _, err := service.Check(context.Background(), time.Now()); !errors.Is(err, want) {
		t.Fatalf("messenger error = %v", err)
	}
	if repository.saves != 0 || len(messenger.texts) != 1 {
		t.Fatalf("failed send was recorded as delivered: saves=%d sends=%d", repository.saves, len(messenger.texts))
	}
	messenger.err = nil
	if _, err := service.Check(context.Background(), time.Now()); err != nil {
		t.Fatalf("retry after failed send = %v", err)
	}
	if repository.saves != 1 || len(messenger.texts) != 2 {
		t.Fatalf("failed send was not retried: saves=%d sends=%d", repository.saves, len(messenger.texts))
	}
}

func TestDurationUsesLegacyAlertLabels(t *testing.T) {
	for value, want := range map[time.Duration]string{
		30 * time.Second: "1 分钟",
		90 * time.Minute: "1 小时 30 分钟",
		48 * time.Hour:   "2 天",
	} {
		if got := duration(value); got != want {
			t.Errorf("duration(%s) = %q, want %q", value, got, want)
		}
	}
}

/** TestBackupFailureFindingEscalatesAfterFiftyHoursWithoutSuccess preserves backup alert timing. */
func TestBackupFailureFindingEscalatesAfterFiftyHoursWithoutSuccess(t *testing.T) {
	now := time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC)
	firstAttempt := now.Add(-50 * time.Hour)
	if findings := backupFailureFinding(now, backupSnapshot{}, nil, &firstAttempt); len(findings) != 0 {
		t.Fatalf("backup at the 50-hour boundary alerted early: %+v", findings)
	}

	firstAttempt = now.Add(-50*time.Hour - time.Second)
	findings := backupFailureFinding(now, backupSnapshot{}, nil, &firstAttempt)
	if len(findings) != 1 || findings[0].Key != "backup.failed" || findings[0].Level != LevelToday {
		t.Fatalf("backup older than 50 hours did not escalate: %+v", findings)
	}
	if !strings.Contains(findings[0].Impact, "还没有成功的完整备份") || !findings[0].Since.Equal(firstAttempt) {
		t.Fatalf("never-successful backup details = %+v", findings[0])
	}
}

/** TestSourceHealthFindingsKeepIntermittentAndUseSpecificFollowUps preserves daily collection alerts. */
func TestSourceHealthFindingsKeepIntermittentAndUseSpecificFollowUps(t *testing.T) {
	cutoff := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	lastError := "HTTP 503"
	sources := []sourcehealth.Source{
		{
			ID:             "editorial-old",
			Name:           "Editorial",
			Mode:           "editorial",
			CreatedAt:      cutoff.Add(-time.Hour),
			Health:         "failing",
			FailCount:      6,
			LastError:      &lastError,
			Runs:           20,
			Failed:         5,
			Undated:        1,
			Repeated:       2,
			DetailFailures: 3,
		},
		{
			ID:             "signal-new",
			Name:           "Signal",
			Mode:           "hot_signal",
			CreatedAt:      cutoff.Add(time.Hour),
			Health:         "ok",
			Runs:           20,
			Failed:         5,
			Undated:        4,
			Repeated:       4,
			DetailFailures: 1,
		},
		{
			ID:        "budget-only",
			Name:      "Budget",
			Mode:      "editorial",
			CreatedAt: cutoff.Add(time.Hour),
		},
	}
	findings := sourceHealthFindings(sources, cutoff)
	byKey := make(map[string]Finding, len(findings))
	for _, finding := range findings {
		byKey[finding.Key] = finding
	}
	for _, key := range []string{
		"sources.failing.editorial",
		"sources.unstable.editorial",
		"sources.silent.editorial",
		"sources.quality.editorial",
		"sources.details.editorial",
		"sources.unstable.hot_signal",
		"sources.details.hot_signal",
	} {
		if _, exists := byKey[key]; !exists {
			t.Errorf("missing source-health finding %q", key)
		}
	}
	if _, exists := byKey["sources.quality.hot_signal"]; exists {
		t.Fatal("hot-signal bodies must not be flagged for editorial article quality")
	}
	if _, exists := byKey["sources.unstable.editorial"]; !exists || strings.Contains(byKey["sources.unstable.editorial"].Detail, "budget-only") {
		t.Fatalf("budget-only source entered unstable follow-up: %#v", byKey["sources.unstable.editorial"])
	}
	if !strings.Contains(byKey["sources.failing.editorial"].Detail, "连续失败 6 次，HTTP 503") ||
		!strings.Contains(byKey["sources.unstable.editorial"].Detail, "近 7 天失败 5/20 次") ||
		!strings.Contains(byKey["sources.silent.editorial"].Detail, "20 次抓取、0 条新发现") ||
		!strings.Contains(byKey["sources.quality.editorial"].Detail, "缺发布时间 1 篇、反复修订 2 篇") ||
		!strings.Contains(byKey["sources.details.hot_signal"].Detail, "近 7 天 1 次") {
		t.Fatalf("source-health details lost their evidence: %#v", byKey)
	}
}

/** TestSourceHealthListKeepsTheTailCountAfterBoundingDetails verifies bounded alert text. */
func TestSourceHealthListKeepsTheTailCountAfterBoundingDetails(t *testing.T) {
	sources := make([]sourcehealth.Source, 12)
	for index := range sources {
		sources[index] = sourcehealth.Source{ID: fmt.Sprintf("source-%02d", index), Name: "Source"}
	}
	detail := sourceHealthList(sources, func(sourcehealth.Source) string {
		return "failed"
	})
	if strings.Contains(detail, "source-11") || !strings.Contains(detail, "另有 2 个，见信源周报与后台信源列表") {
		t.Fatalf("bounded source detail = %q", detail)
	}
}

/** TestWorkerWatchdogRecordsBaselineAlertsAndRecovery preserves the legacy state transitions. */
func TestWorkerWatchdogRecordsBaselineAlertsAndRecovery(t *testing.T) {
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-time.Minute)
	repository := &workerWatchdogRepositoryFixture{heartbeatAt: &heartbeat}
	messenger := &messengerFixture{}
	watchdog := NewWorkerWatchdog(repository, messenger, Config{
		FeishuInternalEnabled: true,
		AlertChatID:           "alerts",
		InternalChatID:        "internal",
		EnvironmentName:       "staging",
	})

	if err := watchdog.Check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if repository.state == nil || repository.state.State != "up" || len(messenger.texts) != 0 {
		t.Fatalf("healthy baseline = state=%+v messages=%d", repository.state, len(messenger.texts))
	}

	heartbeat = now.Add(-31 * time.Minute)
	if err := watchdog.Check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if repository.state.State != "down" || !repository.state.Since.Equal(heartbeat) || len(messenger.texts) != 1 {
		t.Fatalf("stale check = state=%+v messages=%d", repository.state, len(messenger.texts))
	}
	for _, want := range []string{"【staging】🔴 后台处理服务停了", "worker 心跳停在", "docker compose logs go-worker"} {
		if !strings.Contains(messenger.texts[0], want) {
			t.Errorf("worker alert missing %q: %s", want, messenger.texts[0])
		}
	}
	if err := watchdog.Check(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(messenger.texts) != 1 {
		t.Fatalf("stale worker alert repeated: %d", len(messenger.texts))
	}

	recoveredAt := now.Add(2 * time.Minute)
	heartbeat = recoveredAt
	if err := watchdog.Check(context.Background(), recoveredAt); err != nil {
		t.Fatal(err)
	}
	if repository.state.State != "up" || len(messenger.texts) != 2 || !strings.Contains(messenger.texts[1], "✅ 已恢复：后台处理服务停了") {
		t.Fatalf("recovery = state=%+v messages=%v", repository.state, messenger.texts)
	}
}

/** TestWorkerWatchdogRetriesFailedAlertAndHonorsSafetyValve keeps delivery failure and safety semantics. */
func TestWorkerWatchdogRetriesFailedAlertAndHonorsSafetyValve(t *testing.T) {
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-time.Hour)
	repository := &workerWatchdogRepositoryFixture{heartbeatAt: &heartbeat}
	wantErr := errors.New("synthetic send failure")
	messenger := &messengerFixture{err: wantErr}
	watchdog := NewWorkerWatchdog(repository, messenger, Config{FeishuInternalEnabled: true, AlertChatID: "alerts"})
	if err := watchdog.Check(context.Background(), now); !errors.Is(err, wantErr) {
		t.Fatalf("failed alert error = %v", err)
	}
	if repository.state != nil || repository.saves != 0 {
		t.Fatalf("failed alert advanced state: state=%+v saves=%d", repository.state, repository.saves)
	}

	messenger.err = nil
	if err := watchdog.Check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if repository.state == nil || repository.state.State != "down" || repository.saves != 1 || len(messenger.texts) != 2 {
		t.Fatalf("retry did not record alert: state=%+v saves=%d messages=%d", repository.state, repository.saves, len(messenger.texts))
	}

	missingHeartbeat := &workerWatchdogRepositoryFixture{}
	disabled := NewWorkerWatchdog(missingHeartbeat, messenger, Config{})
	if err := disabled.Check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if missingHeartbeat.state != nil || missingHeartbeat.saves != 0 {
		t.Fatalf("missing heartbeat changed state: state=%+v saves=%d", missingHeartbeat.state, missingHeartbeat.saves)
	}

	disabledRepo := &workerWatchdogRepositoryFixture{heartbeatAt: &heartbeat}
	disabled = NewWorkerWatchdog(disabledRepo, messenger, Config{})
	if err := disabled.Check(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if disabledRepo.state == nil || disabledRepo.state.State != "down" || len(messenger.texts) != 2 {
		t.Fatalf("disabled delivery should record state without sending: state=%+v messages=%d", disabledRepo.state, len(messenger.texts))
	}
}
