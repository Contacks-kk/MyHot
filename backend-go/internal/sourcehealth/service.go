package sourcehealth

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

type Messenger interface {
	SendText(context.Context, string, string) error
}

type Config struct {
	Enabled         bool
	AlertChatID     string
	InternalChatID  string
	EnvironmentName string
}

type Result struct {
	Failing int `json:"failing"`
	Silent  int `json:"silent"`
}

type Service struct {
	repository Repository
	messenger  Messenger
	config     Config
	now        func() time.Time
}

func NewService(repository Repository, messenger Messenger, cfg Config) *Service {
	return &Service{repository: repository, messenger: messenger, config: cfg, now: time.Now}
}

func (s *Service) Due(ctx context.Context, now time.Time) (string, bool, error) {
	due, next, err := sourceHealthWeek(now)
	if err != nil || now.Before(due) {
		return "", false, err
	}
	found, err := s.repository.HasRun(ctx, due, next)
	if err != nil || found {
		return "", false, err
	}
	return due.Format("2006-01-02"), true, nil
}

func (s *Service) Run(ctx context.Context, now time.Time) (Result, error) {
	if now.IsZero() {
		if s.now != nil {
			now = s.now()
		} else {
			now = time.Now()
		}
	}
	now = now.UTC()
	since, before := now.Add(-7*24*time.Hour), now.Add(-14*24*time.Hour)
	snapshot, err := s.repository.Weekly(ctx, since, before)
	if err != nil {
		return Result{}, err
	}
	groups := groupSources(snapshot.Sources, since)
	lines := []string{
		fmt.Sprintf("本周收录 %s 条（上周 %s，%s），其中精选 %s 条", comma(snapshot.ThisWeek), comma(snapshot.LastWeek), percent(snapshot.ThisWeek, snapshot.LastWeek), comma(snapshot.Selected)),
		fmt.Sprintf("在用信源 %s 个，本周新增 %s 个；抓取失败 %s 个，不太稳定 %s 个", comma(snapshot.Enabled), comma(snapshot.Added), comma(snapshot.Failing), comma(snapshot.Degraded)),
	}
	for _, group := range groups {
		items, failing, unstable, silent, quality, detailFailures := groupStats(group)
		lines = append(lines, "", fmt.Sprintf("%s：%s 个在用信源；本周归属条目 %s 条；连续失败 %s 个、反复失败 %s 个、7 天无新发现 %s 个", group.name, comma(len(group.sources)), comma(items), comma(len(failing)), comma(len(unstable)), comma(len(silent))))
		if len(failing) > 0 {
			lines = append(lines, "抓取失败："+sourceList(failing, func(source Source) string {
				if source.LastError == nil || *source.LastError == "" {
					return fmt.Sprintf("连续失败 %s 次", comma(source.FailCount))
				}
				return fmt.Sprintf("连续失败 %s 次：%s", comma(source.FailCount), *source.LastError)
			}))
		}
		if len(unstable) > 0 {
			lines = append(lines, "不稳定："+sourceList(unstable, func(source Source) string {
				return fmt.Sprintf("失败 %s/%s 次", comma(source.Failed), comma(source.Runs))
			}))
		}
		if len(silent) > 0 {
			lines = append(lines, "7 天无新发现（需对照原站，可能只是低频更新）："+sourceList(silent, func(source Source) string {
				return fmt.Sprintf("%s 次抓取", comma(source.Runs))
			}))
		}
		if len(quality) > 0 {
			lines = append(lines, "文章质量待核实："+sourceList(quality, func(source Source) string {
				return fmt.Sprintf("缺发布时间 %s 篇、反复修订 %s 篇", comma(source.Undated), comma(source.Repeated))
			}))
		}
		if len(detailFailures) > 0 {
			lines = append(lines, "详情补全失败："+sourceList(detailFailures, func(source Source) string {
				return fmt.Sprintf("%s 次", comma(source.DetailFailures))
			}))
		}
	}
	failing := snapshot.Failing
	silent := 0
	followUp := failing > 0
	for _, group := range groups {
		_, groupFailing, unstable, groupSilent, quality, details := groupStats(group)
		silent += len(groupSilent)
		followUp = followUp || len(groupFailing)+len(unstable)+len(groupSilent)+len(quality)+len(details) > 0
	}
	if followUp {
		lines = append(lines, "", "需要处理的话，把这条转给 AI；详情在后台“信源”与“运行”页。")
	} else {
		lines = append(lines, "", "没有需要处理的信源。")
	}
	chat := strings.TrimSpace(s.config.AlertChatID)
	if chat == "" {
		chat = strings.TrimSpace(s.config.InternalChatID)
	}
	if !s.config.Enabled || chat == "" {
		return Result{Failing: failing, Silent: silent}, nil
	}
	if s.messenger == nil {
		return Result{}, fmt.Errorf("source-health messenger is unavailable")
	}
	prefix := ""
	if s.config.EnvironmentName != "" && s.config.EnvironmentName != "production" {
		prefix = "[" + s.config.EnvironmentName + "] "
	}
	if err := s.messenger.SendText(ctx, chat, prefix+"📊 信源周报\n"+strings.Join(lines, "\n")); err != nil {
		return Result{}, err
	}
	return Result{Failing: failing, Silent: silent}, nil
}

type sourceGroup struct {
	name    string
	since   time.Time
	sources []Source
}

func groupSources(sources []Source, since time.Time) []sourceGroup {
	groups := []sourceGroup{{name: "编辑内容", since: since}, {name: "热度信号", since: since}}
	for _, source := range sources {
		switch source.Mode {
		case "editorial":
			groups[0].sources = append(groups[0].sources, source)
		case "hot_signal":
			groups[1].sources = append(groups[1].sources, source)
		}
	}
	return groups
}

func groupStats(group sourceGroup) (items int, failing, unstable, silent, quality, detailFailures []Source) {
	for _, source := range group.sources {
		items += source.Items
		if source.Health == "failing" {
			failing = append(failing, source)
		}
		if source.Failed >= 5 && source.Runs > 0 && float64(source.Failed)/float64(source.Runs) >= 0.1 {
			unstable = append(unstable, source)
		}
		if source.CreatedAt.Before(group.since) && source.Discoveries == 0 {
			silent = append(silent, source)
		}
		if group.name == "编辑内容" && (source.Undated > 0 || source.Repeated > 0) {
			quality = append(quality, source)
		}
		if source.DetailFailures > 0 {
			detailFailures = append(detailFailures, source)
		}
	}
	return items, failing, unstable, silent, quality, detailFailures
}

func sourceList(sources []Source, describe func(Source) string) string {
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, fmt.Sprintf("%s（%s）：%s", source.Name, source.ID, describe(source)))
	}
	return strings.Join(parts, "；")
}

func percent(current, previous int) string {
	if previous == 0 {
		return "—"
	}
	change := int(math.Round(float64(current-previous) / float64(previous) * 100))
	if change >= 0 {
		return fmt.Sprintf("+%d%%", change)
	}
	return fmt.Sprintf("%d%%", change)
}

func comma(value int) string {
	negative := value < 0
	if negative {
		value = -value
	}
	text := fmt.Sprintf("%d", value)
	for index := len(text) - 3; index > 0; index -= 3 {
		text = text[:index] + "," + text[index:]
	}
	if negative {
		return "-" + text
	}
	return text
}

func sourceHealthWeek(now time.Time) (time.Time, time.Time, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	local := now.In(location)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	week := time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, location).AddDate(0, 0, -daysSinceMonday)
	return week.UTC(), week.AddDate(0, 0, 7).UTC(), nil
}
