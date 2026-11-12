package operationsalerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
	service = NewService(&repositoryFixture{findings: []Finding{{Key: "x", Level: LevelNow, Title: "x"}}}, &messengerFixture{err: want}, Config{FeishuInternalEnabled: true, AlertChatID: "alerts"})
	if _, err := service.Check(context.Background(), time.Now()); !errors.Is(err, want) {
		t.Fatalf("messenger error = %v", err)
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
