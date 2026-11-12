package operationsalerts

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Level string

const (
	LevelNow   Level = "now"
	LevelToday Level = "today"
)

type Finding struct {
	Key    string
	Level  Level
	Title  string
	Impact string
	Heals  string
	Action string
	Detail string
	Since  *time.Time
}

type StateEntry struct {
	Title  string    `json:"title"`
	Since  time.Time `json:"since"`
	SentAt time.Time `json:"sentAt"`
}

type State map[string]StateEntry

type Config struct {
	CollectEnabled        bool
	ModelCallsEnabled     bool
	BackupConfigured      bool
	FeishuInternalEnabled bool
	AlertChatID           string
	InternalChatID        string
	EnvironmentName       string
	QuietMinutes          int
	DailyEdition          string
}

type Repository interface {
	Findings(context.Context, time.Time, Config) ([]Finding, error)
	LoadState(context.Context) (State, error)
	SaveState(context.Context, State) error
}

type Messenger interface {
	SendText(context.Context, string, string) error
}

type Service struct {
	repository Repository
	messenger  Messenger
	config     Config
}

func NewService(repository Repository, messenger Messenger, cfg Config) *Service {
	if cfg.DailyEdition == "" {
		cfg.DailyEdition = "08:00"
	}
	return &Service{repository: repository, messenger: messenger, config: cfg}
}

type Result struct {
	Open []string `json:"open"`
	Sent []string `json:"sent"`
}

func (s *Service) Check(ctx context.Context, now time.Time) (Result, error) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	found, err := s.repository.Findings(ctx, now, s.config)
	if err != nil {
		return Result{}, err
	}
	state, err := s.repository.LoadState(ctx)
	if err != nil {
		return Result{}, err
	}
	if state == nil {
		state = State{}
	}
	active := make(map[string]Finding, len(found))
	sent := make([]string, 0, len(found))
	for _, finding := range found {
		if finding.Key == "" || finding.Title == "" {
			continue
		}
		active[finding.Key] = finding
		open, exists := state[finding.Key]
		if exists && now.Sub(open.SentAt) <= repeatAfter(finding.Level) {
			continue
		}
		since := now
		if exists {
			since = open.Since
		} else if finding.Since != nil {
			since = finding.Since.UTC()
		}
		title, lines := formatAlert(finding, since, now, exists)
		if err := s.send(ctx, title, lines); err != nil {
			return Result{}, err
		}
		state[finding.Key] = StateEntry{Title: finding.Title, Since: since, SentAt: now}
		sent = append(sent, finding.Key)
	}
	for key, open := range state {
		if _, stillOpen := active[key]; stillOpen {
			continue
		}
		title := "✅ 已恢复：" + open.Title
		lines := []string{"持续 " + duration(now.Sub(open.Since)) + "（" + beijingStamp(open.Since) + " 起）"}
		if err := s.send(ctx, title, lines); err != nil {
			return Result{}, err
		}
		sent = append(sent, key+":recovered")
		delete(state, key)
	}
	if err := s.repository.SaveState(ctx, state); err != nil {
		return Result{}, err
	}
	open := make([]string, 0, len(state))
	for key := range state {
		open = append(open, key)
	}
	return Result{Open: open, Sent: sent}, nil
}

func (s *Service) send(ctx context.Context, title string, lines []string) error {
	if !s.config.FeishuInternalEnabled {
		return nil
	}
	chat := strings.TrimSpace(s.config.AlertChatID)
	if chat == "" {
		chat = strings.TrimSpace(s.config.InternalChatID)
	}
	if chat == "" {
		return nil
	}
	if s.messenger == nil {
		return fmt.Errorf("operations alert messenger is unavailable")
	}
	prefix := ""
	if s.config.EnvironmentName != "" && s.config.EnvironmentName != "production" {
		prefix = "【" + s.config.EnvironmentName + "】"
	}
	return s.messenger.SendText(ctx, chat, prefix+title+"\n"+strings.Join(lines, "\n"))
}

func repeatAfter(level Level) time.Duration {
	if level == LevelToday {
		return 24 * time.Hour
	}
	return time.Hour
}

func formatAlert(f Finding, since, now time.Time, repeat bool) (string, []string) {
	mark := "🔴"
	if f.Level == LevelToday {
		mark = "🟠"
	}
	lasting := ""
	if now.Sub(since) >= time.Minute {
		lasting = "（已持续 " + duration(now.Sub(since)) + "）"
	}
	prefix := ""
	if repeat {
		prefix = "仍未恢复："
	}
	lines := make([]string, 0, 4)
	for _, line := range []string{
		optionalLine("影响：", f.Impact), optionalLine("会自己好吗：", f.Heals),
		optionalLine("你需要：", f.Action), optionalLine("给 AI 的细节：", f.Detail),
	} {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return mark + " " + prefix + f.Title + lasting, lines
}

func optionalLine(label, value string) string {
	if value == "" {
		return ""
	}
	return label + value
}

func duration(value time.Duration) string {
	minutes := int((value + 30*time.Second) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	if minutes < 48*60 {
		hours := minutes / 60
		if remainder := minutes % 60; remainder != 0 {
			return fmt.Sprintf("%d 小时 %d 分钟", hours, remainder)
		}
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 天", minutes/1440)
}

func beijingStamp(at time.Time) string {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return at.In(location).Format("1月2日 15:04")
}
