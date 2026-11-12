package reports

import (
	"context"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
)

type reportKeyFixture struct {
	ReportRepository
	keys map[string][]string
}

func (r reportKeyFixture) ReportKeys(_ context.Context, kind string) ([]string, error) {
	return r.keys[kind], nil
}

func TestDueReportDatesUseBeijingEditionBoundaries(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		now   time.Time
		wantD string
		wantW string
		wantM string
	}{
		{name: "before daily", now: time.Date(2026, 10, 8, 7, 59, 59, 0, location), wantD: "2026-10-07", wantW: "2026-W40", wantM: "2026-09"},
		{name: "after daily", now: time.Date(2026, 10, 8, 8, 0, 0, 0, location), wantD: "2026-10-08", wantW: "2026-W40", wantM: "2026-09"},
		{name: "before weekly", now: time.Date(2026, 10, 5, 9, 59, 59, 0, location), wantD: "2026-10-05", wantW: "2026-W39", wantM: "2026-09"},
		{name: "after weekly", now: time.Date(2026, 10, 5, 10, 0, 0, 0, location), wantD: "2026-10-05", wantW: "2026-W40", wantM: "2026-09"},
		{name: "first day before monthly", now: time.Date(2026, 10, 1, 10, 29, 59, 0, location), wantD: "2026-10-01", wantW: "2026-W39", wantM: "2026-08"},
		{name: "first day after monthly", now: time.Date(2026, 10, 1, 10, 30, 0, 0, location), wantD: "2026-10-01", wantW: "2026-W39", wantM: "2026-09"},
	} {
		t.Run(test.name, func(t *testing.T) {
			daily, err := dueDaily(test.now, "08:00", location)
			if err != nil || daily != test.wantD {
				t.Fatalf("due daily = %q, %v; want %q", daily, err, test.wantD)
			}
			weekly, err := dueWeekly(test.now, "10:00", location)
			if err != nil || weekly != test.wantW {
				t.Fatalf("due weekly = %q, %v; want %q", weekly, err, test.wantW)
			}
			monthly, err := dueMonthly(test.now, "10:30", location)
			if err != nil || monthly != test.wantM {
				t.Fatalf("due monthly = %q, %v; want %q", monthly, err, test.wantM)
			}
		})
	}
}

func TestISOWeekAndMonthRanges(t *testing.T) {
	start, end, ok := isoWeekRange("2024-W05")
	if !ok || start != "2024-01-29" || end != "2024-02-04" {
		t.Fatalf("week range = %q..%q ok=%v", start, end, ok)
	}
	if _, _, ok := isoWeekRange("2021-W53"); ok {
		t.Fatal("invalid week 53 was accepted")
	}
	start, end, ok = monthRange("2024-02")
	if !ok || start != "2024-02-01" || end != "2024-02-29" {
		t.Fatalf("month range = %q..%q ok=%v", start, end, ok)
	}
}

func TestReportPromptFilesRenderIndustrySectionsAndHashSourceText(t *testing.T) {
	files, err := readPromptFiles("../../../shared/industry/prompts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(files.Version, "report-period+report-period-sections+report-period-no-sections@") {
		t.Fatalf("prompt version = %q", files.Version)
	}
	system, err := files.PeriodSystem("Example", "周报", "一周", "三句话", "160", []string{"模型发布/更新", "行业动态"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Example", "模型发布/更新", "行业动态", "160", `"overview"`} {
		if !strings.Contains(system, want) {
			t.Errorf("rendered prompt is missing %q", want)
		}
	}
	if strings.Contains(system, "{{") {
		t.Fatal("unfilled prompt variable remains")
	}
}

func TestGroundedPeriodTextOnlyNamesFactsInTheIssue(t *testing.T) {
	entity := interests.Entity{Name: "OpenAI", Aliases: []string{"ChatGPT"}}
	google := interests.Entity{Name: "Google", Aliases: []string{"谷歌"}}
	service, err := NewService(reportKeyFixture{}, nil, Config{
		SiteName: "Example", DailyTime: "08:00", WeeklyTime: "10:00", MonthlyTime: "10:30",
		Categories: []Category{{Key: "industry", Section: "行业动态"}}, Entities: map[string]interests.Entity{"openai": entity, "google": google},
		Reporting: ReportingConfig{PlainTerms: []string{"api"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !service.grounded("OpenAI 发布 GPT-5，新增 100 个 API。", "OpenAI 发布 GPT-5，新增 100 个 API。") {
		t.Fatal("fact-grounded company, model and number were rejected")
	}
	for _, text := range []string{"Acme 发布新模型。", "OpenAI 发布了 200 个 API。", "谷歌推出新模型。"} {
		if service.grounded(text, "OpenAI 发布 GPT-5，新增 100 个 API。") {
			t.Errorf("ungrounded text was accepted: %q", text)
		}
	}
}

func TestFittedKeepsOnlyLeadingWholeSentences(t *testing.T) {
	if got := fitted("第一句。第二句！第三句", 5); got != "第一句。" {
		t.Fatalf("fitted = %q", got)
	}
	if got := fitted("超长句子不能截断。", 3); got != "" {
		t.Fatalf("overlong first sentence = %q", got)
	}
}

func TestComposeDueDoesNotRegenerateExistingReports(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	repo := reportKeyFixture{keys: map[string][]string{
		"daily": {"2026-10-08"}, "weekly": {"2026-W40"}, "monthly": {"2026-09"},
	}}
	service, err := NewService(repo, nil, Config{SiteName: "Example", DailyTime: "08:00", WeeklyTime: "10:00", MonthlyTime: "10:30",
		Categories: []Category{{Key: "industry", Section: "行业动态"}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ComposeDue(context.Background(), time.Date(2026, 10, 8, 11, 0, 0, 0, location), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(result["generated"].([]string)) != 0 || len(result["failed"].([]string)) != 0 {
		t.Fatalf("compose due existing reports = %#v", result)
	}
}

func TestArrangeDailyFoldsRoundupsAndLimitsSourceCoverage(t *testing.T) {
	story1, story2 := int64(1), int64(2)
	category := "industry"
	previous := PreviousStory{Key: "2026-10-07", Title: "Earlier story"}
	main := EditionEntry{Entry: Entry{ItemID: "main", SourceID: "source-a", Sources: 1}, Category: &category,
		StoryID: &story1, Mentions: map[int64]struct{}{}, SourceIDs: map[string]struct{}{"source-a": {}}, Importance: 20}
	roundup := EditionEntry{Entry: Entry{ItemID: "roundup", SourceID: "source-b"}, Category: &category,
		Mentions: map[int64]struct{}{story1: {}}, SourceIDs: map[string]struct{}{"source-b": {}}, Importance: 10}
	followup := EditionEntry{Entry: Entry{ItemID: "followup", SourceID: "source-a", Sources: 1}, Category: &category,
		StoryID: &story2, Mentions: map[int64]struct{}{}, SourceIDs: map[string]struct{}{"source-a": {}}, Authority: 3, Previous: &previous, Importance: 15}
	result := arrangeDaily([]EditionEntry{main, followup, roundup}, map[string]string{"industry": "行业动态"}, []Category{{Key: "industry", Section: "行业动态"}})
	if len(result.Main) != 1 || result.Main[0].Entry.ItemID != "main" || result.Main[0].Entry.Sources != 2 || len(result.Main[0].Entry.Related) != 1 {
		t.Fatalf("folded roundup = %#v", result.Main)
	}
	if len(result.Flashes) != 1 || result.Flashes[0].Entry.ItemID != "followup" || result.Stats["followUpsAsFlashes"] != 1 {
		t.Fatalf("follow-up flash behavior = %#v, stats=%v", result.Flashes, result.Stats)
	}
}
