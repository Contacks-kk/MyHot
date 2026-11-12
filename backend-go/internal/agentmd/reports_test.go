package agentmd

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/publiccontent"
)

type reportFixture struct {
	daily      *publiccontent.V1DailyResponse
	notes      map[string]publiccontent.V1DailyAgentNote
	period     *publiccontent.V1PeriodResponse
	dailyCalls []string
	periodArgs []any
}

func (f *reportFixture) HotTopics(context.Context, time.Time, string, string) (publiccontent.HotTopicsResponse, error) {
	return publiccontent.HotTopicsResponse{}, nil
}

func (f *reportFixture) V1DailyWithAgentNotes(_ context.Context, key string, latest bool, _ time.Time, _, _ string) (*publiccontent.V1DailyResponse, map[string]publiccontent.V1DailyAgentNote, error) {
	f.dailyCalls = append(f.dailyCalls, key)
	f.dailyCalls = append(f.dailyCalls, map[bool]string{true: "latest", false: "exact"}[latest])
	return f.daily, f.notes, nil
}

func (f *reportFixture) V1Period(_ context.Context, kind, key string, latest bool, _ time.Time, _, _, measure, noun string) (*publiccontent.V1PeriodResponse, error) {
	f.periodArgs = append(f.periodArgs, kind, key, latest, measure, noun)
	return f.period, nil
}

func TestDailyAnswerRendersNotesLeadAndFixedReportSemantics(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	published := "2026-10-07T02:00:00.000Z"
	related := "https://site.invalid/items/related"
	hot := "https://site.invalid/items/a1"
	var report publiccontent.V1DailyBody
	report.Date = "2026-10-07"
	report.WindowStart = "2026-10-06T00:00:00.000Z"
	report.WindowEnd = "2026-10-07T00:00:00.000Z"
	report.Links.AIHot = "https://site.invalid/daily/2026-10-07"
	report.Lead = &publiccontent.V1DailyLead{Title: "Lead", LeadParagraph: "Lead summary"}
	report.Sections = []publiccontent.V1DailySection{{Label: "模型", Items: []publiccontent.V1DailyItemCitation{{
		Title: "Lead", Summary: "Lead summary", Source: publiccontent.V1ReportSource{Name: "X：Researcher (@researcher)"},
		Links: publiccontent.V1ReportItemLink{AIHot: &hot, Original: "https://source.invalid/a1"},
	}}}}
	report.Flashes = []publiccontent.V1DailyFlashCitation{{
		Title: "Flash", Source: publiccontent.V1ReportSource{Name: "News"},
		Links: publiccontent.V1ReportItemLink{Original: "https://source.invalid/flash"}, PublishedAt: published,
	}}
	followUp := "2026-10-01"
	answer := DailyAnswer(report, map[string]publiccontent.V1DailyAgentNote{hot: {
		OtherSources: 2, FollowUp: &followUp, Related: []publiccontent.V1DailyAgentRelated{{Title: "Earlier", Link: related}},
	}}, "Example", "https://site.invalid", now)
	for _, want := range []string{
		"# Example 日报 · 2026-10-07（星期三）",
		"收录北京时间 10-06 08:00 至 10-07 08:00 的动态，每天 08:00 发布。",
		"头条：Lead", "【模型】", "另有 2 家信源报道", "跟进：2026-10-01 的日报报道过这件事，这里是新进展",
		"[Earlier](https://site.invalid/items/related)", "- 10-07 10:00 · [Flash](https://source.invalid/flash) · News",
		"日报是每天 08:00 发布的固定成品，不等于“过去 24 小时”的滚动列表。",
	} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestDailyAnswerEmptyOutputAndLeadFallback(t *testing.T) {
	var report publiccontent.V1DailyBody
	report.Date = "2026-10-07"
	report.Links.AIHot = "https://site.invalid/daily/2026-10-07"
	report.Lead = &publiccontent.V1DailyLead{Title: "Written lead", LeadParagraph: "Lead text"}
	answer := DailyAnswer(report, nil, "Example", "https://site.invalid", time.Now())
	if !strings.Contains(answer, "导语：Written lead") || strings.Contains(answer, "这一期暂时没有可以展示的条目。") || !strings.Contains(answer, "不可信外部资料开始") {
		t.Fatalf("answer = %s", answer)
	}
}

func TestPeriodAnswerPreservesIssueLinksCitationsAndInstructions(t *testing.T) {
	week := "2026-W40"
	headline := "Weekly headline"
	overview := "Weekly overview"
	summary := "Section guide"
	published := "2026-10-04T20:00:00.000Z"
	itemURL := "https://site.invalid/items/a1"
	report := publiccontent.V1PeriodBody{
		Week: &week, PeriodStart: "2026-09-28", PeriodEnd: "2026-10-04",
		Links:    publiccontent.V1ReportLink{AIHot: "https://site.invalid/weekly/2026-W40"},
		Headline: &headline, Overview: &overview,
		Sections: []publiccontent.V1PeriodSection{{Label: "公司", Summary: &summary, Items: []publiccontent.V1PeriodCitation{{
			Title: "Report [one]", Summary: "Details", Source: publiccontent.V1ReportSource{Name: "Company"},
			Links: publiccontent.V1ReportItemLink{AIHot: &itemURL, Original: "https://source.invalid/a1"}, PublishedAt: &published,
		}}}},
	}
	answer := PeriodAnswer(report, "weekly", "Example", "https://site.invalid")
	for _, want := range []string{
		"# Example 周报 · 2026-W40", "从 2026-09-28 至 2026-10-04 的日报里选出的重点，每周一 10:00（北京时间）发布。",
		"头条：Weekly headline", "总述：Weekly overview", "【公司】", "导读：Section guide",
		"[Report \\[one\\]](https://site.invalid/items/a1) · Company（10-05）",
		"最近一周」的滚动列表", "https://site.invalid/api/v1/agent/weekly/YYYY-Www",
	} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer missing %q:\n%s", want, answer)
		}
	}
}

func TestAgentReportServiceUsesSharedGoReportProvider(t *testing.T) {
	daily := &publiccontent.V1DailyResponse{Report: publiccontent.V1DailyBody{Date: "2026-10-07"}}
	month := "2026-09"
	period := &publiccontent.V1PeriodResponse{Report: publiccontent.V1PeriodBody{Month: &month}}
	fixture := &reportFixture{daily: daily, period: period, notes: map[string]publiccontent.V1DailyAgentNote{}}
	service := NewService(fixture, "Example", "https://site.invalid")
	if _, found, err := service.Daily(context.Background(), "latest", true, time.Now()); err != nil || !found || !reflect.DeepEqual(fixture.dailyCalls, []string{"latest", "latest"}) {
		t.Fatalf("daily found=%v calls=%#v error=%v", found, fixture.dailyCalls, err)
	}
	if _, found, err := service.Period(context.Background(), "monthly", "2026-09", false, time.Now()); err != nil || !found || !reflect.DeepEqual(fixture.periodArgs, []any{"monthly", "2026-09", false, "件", "大事"}) {
		t.Fatalf("period found=%v args=%#v error=%v", found, fixture.periodArgs, err)
	}
}
