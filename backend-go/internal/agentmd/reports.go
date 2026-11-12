package agentmd

import (
	"context"
	"errors"
	"strings"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

type AgentReportProvider interface {
	V1DailyWithAgentNotes(context.Context, string, bool, time.Time, string, string) (*publiccontent.V1DailyResponse, map[string]publiccontent.V1DailyAgentNote, error)
	V1Period(context.Context, string, string, bool, time.Time, string, string, string, string) (*publiccontent.V1PeriodResponse, error)
}

func (s *Service) Daily(ctx context.Context, key string, latest bool, now time.Time) (string, bool, error) {
	if s.reports == nil {
		return "", false, errors.New("v1 report provider is unavailable")
	}
	report, notes, err := s.reports.V1DailyWithAgentNotes(ctx, key, latest, now, s.siteURL, s.siteName)
	if err != nil || report == nil {
		return "", false, err
	}
	return DailyAnswer(report.Report, notes, s.siteName, s.siteURL, now), true, nil
}

func (s *Service) Period(ctx context.Context, kind, key string, latest bool, now time.Time) (string, bool, error) {
	if s.reports == nil {
		return "", false, errors.New("v1 report provider is unavailable")
	}
	report, err := s.reports.V1Period(ctx, kind, key, latest, now, s.siteURL, s.siteName, s.reportMeasure, s.reportNoun)
	if err != nil || report == nil {
		return "", false, err
	}
	return PeriodAnswer(report.Report, kind, s.siteName, s.siteURL), true, nil
}

func DailyAnswer(report publiccontent.V1DailyBody, notes map[string]publiccontent.V1DailyAgentNote, siteName, siteURL string, now time.Time, options ...MCPAnswerOptions) string {
	option := mcpAnswerOptions(options)
	data := make([]string, 0)
	ownLead := false
	if report.Lead != nil {
		for _, section := range report.Sections {
			for _, item := range section.Items {
				if item.Title == report.Lead.Title && item.Summary == report.Lead.LeadParagraph {
					ownLead = true
					break
				}
			}
		}
		if ownLead {
			data = append(data, "头条："+report.Lead.Title, "")
		} else {
			data = append(data, "导语："+report.Lead.Title, report.Lead.LeadParagraph, "")
		}
	}
	for _, section := range report.Sections {
		data = append(data, "【"+section.Label+"】")
		for index, item := range section.Items {
			link := item.Links.Original
			if item.Links.AIHot != nil {
				link = *item.Links.AIHot
			}
			note, noted := notes[link]
			noteSuffix := ""
			if noted && note.OtherSources > 0 {
				noteSuffix = " · 另有 " + intText(note.OtherSources) + " 家信源报道"
			}
			data = append(data, intText(index+1)+". ["+linkText(item.Title)+"]("+link+") · "+publicSourceName(item.Source.Name)+noteSuffix)
			if item.Summary != "" {
				data = append(data, "   "+item.Summary)
			}
			if noted {
				data = append(data, dailyNoteLines(note)...)
			}
		}
		data = append(data, "")
	}
	if len(report.Flashes) > 0 {
		data = append(data, "【快讯】")
		for _, flash := range report.Flashes {
			link := flash.Links.Original
			if flash.Links.AIHot != nil {
				link = *flash.Links.AIHot
			}
			data = append(data, "- "+stamp(flash.PublishedAt, now)+" · ["+linkText(flash.Title)+"]("+link+") · "+publicSourceName(flash.Source.Name))
		}
		data = append(data, "")
	}
	weekday := weekdayName(report.Date)
	url := strings.TrimRight(siteURL, "/") + "/api/v1/agent/daily/"
	head := []string{
		"# " + siteName + " 日报 · " + report.Date + "（" + weekday + "）",
		"",
		"收录北京时间 " + stamp(report.WindowStart, now) + " 至 " + stamp(report.WindowEnd, now) + " 的动态，每天 " + option.DailyTime + " 发布。日报页：" + report.Links.AIHot,
	}
	var content []string
	if len(data) == 0 {
		head = append(head, "这一期暂时没有可以展示的条目。")
	} else {
		content = data
	}
	otherDaily := "要其它日期的日报，请求 " + url + "YYYY-MM-DD（真实日期）；没有就如实说，不要换一天冒充。"
	if option.Enabled {
		otherDaily = "要其它日期的日报，传 date=YYYY-MM-DD（真实日期）；没有就如实说，不要换一天冒充。"
	}
	return answer(siteName, head, content, []string{
		"先讲头条，再按栏目挑重点；用户要全文再全部列出。每条是一件事，「相关」是同一件事的其他进展或同一场发布的其他内容。",
		"日报是每天 " + option.DailyTime + " 发布的固定成品，不等于“过去 24 小时”的滚动列表。",
		otherDaily,
		"不要展示接口地址、参数、User-Agent 这类技术细节。",
	})
}

func PeriodAnswer(report publiccontent.V1PeriodBody, kind, siteName, siteURL string, options ...MCPAnswerOptions) string {
	option := mcpAnswerOptions(options)
	name, edition, noun := "周报", "每周一 "+option.WeeklyTime, "周"
	key := ""
	if report.Week != nil {
		key = *report.Week
	}
	if kind == "monthly" {
		name, edition, noun = "月报", "每月 1 日 "+option.MonthlyTime, "月"
		if report.Month != nil {
			key = *report.Month
		}
	}
	days := " " + key + " "
	if report.PeriodStart != "" && report.PeriodEnd != "" {
		days = " " + report.PeriodStart + " 至 " + report.PeriodEnd + " "
	}
	data := make([]string, 0)
	if report.Headline != nil && *report.Headline != "" {
		data = append(data, "头条："+*report.Headline)
	}
	if report.Overview != nil && *report.Overview != "" {
		data = append(data, "总述："+*report.Overview)
	}
	if len(data) > 0 {
		data = append(data, "")
	}
	for _, section := range report.Sections {
		data = append(data, "【"+section.Label+"】")
		if section.Summary != nil && *section.Summary != "" {
			data = append(data, "导读："+*section.Summary)
		}
		for index, item := range section.Items {
			link := item.Links.Original
			if item.Links.AIHot != nil {
				link = *item.Links.AIHot
			}
			when := ""
			if item.PublishedAt != nil {
				when = "（" + beijingDate(*item.PublishedAt)[5:] + "）"
			}
			data = append(data, intText(index+1)+". ["+linkText(item.Title)+"]("+link+") · "+publicSourceName(item.Source.Name)+when)
			if item.Summary != "" {
				data = append(data, "   "+item.Summary)
			}
		}
		data = append(data, "")
	}
	dataURL := strings.TrimRight(siteURL, "/") + "/api/v1/agent/" + kind + "/"
	head := []string{
		"# " + siteName + " " + name + " · " + key,
		"",
		"从" + days + "的日报里选出的重点，" + edition + "（北京时间）发布。" + name + "页：" + report.Links.AIHot,
	}
	if len(data) == 0 {
		head = append(head, "这一期暂时没有可以展示的条目。")
	}
	lookback := "周"
	keyFormat := "YYYY-Www"
	if kind == "monthly" {
		lookback, keyFormat = "个月", "YYYY-MM"
	}
	otherPeriod := "要其它" + noun + "的" + name + "，请求 " + dataURL + keyFormat + "（真实的" + map[string]string{"weekly": "周", "monthly": "月份"}[kind] + "）；没有就如实说，不要换一期冒充。"
	if option.Enabled {
		parameter, format := "week", "YYYY-Www"
		if kind == "monthly" {
			parameter, format = "month", "YYYY-MM"
		}
		otherPeriod = "要其它" + noun + "的" + name + "，传 " + parameter + "=" + format + "（真实的" + map[string]string{"weekly": "周", "monthly": "月份"}[kind] + "）；没有就如实说，不要换一期冒充。"
	}
	return answer(siteName, head, dataIfAny(data), []string{
		"先讲头条和总述，再按栏目挑重点；用户要全文再全部列出。",
		name + "是从当期日报里按影响力挑出、按栏目编好的固定成品，不等于「最近一" + lookback + "」的滚动列表。",
		otherPeriod,
		"不要展示接口地址、参数、User-Agent 这类技术细节。",
	})
}

func dailyNoteLines(note publiccontent.V1DailyAgentNote) []string {
	lines := make([]string, 0, 1+len(note.Related))
	if note.FollowUp != nil && *note.FollowUp != "" {
		lines = append(lines, "   跟进："+*note.FollowUp+" 的日报报道过这件事，这里是新进展")
	}
	for _, related := range note.Related[:min(4, len(note.Related))] {
		lines = append(lines, "   - 相关：["+linkText(related.Title)+"]("+related.Link+")")
	}
	return lines
}

func dataIfAny(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	return lines
}

func weekdayName(date string) string {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}[parsed.Weekday()]
}

func beijingDate(raw string) string {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return parsed.UTC().Add(8 * time.Hour).Format("2006-01-02")
}
