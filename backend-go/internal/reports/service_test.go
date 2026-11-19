package reports

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
)

/** TestReportingConfigBundledPathPreservesRelease verifies the relocated bundled report configuration. */
func TestReportingConfigBundledPathPreservesRelease(t *testing.T) {
	t.Setenv("INDUSTRY_REPORTING_FILE", "")
	want, err := LoadReportingConfig()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDUSTRY_REPORTING_FILE", "../../config/reporting.json")
	got, err := LoadReportingConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("report configuration changed after relocation")
	}
}

type reportKeyFixture struct {
	ReportRepository
	keys map[string][]string
}

func (r reportKeyFixture) ReportKeys(_ context.Context, kind string) ([]string, error) {
	return r.keys[kind], nil
}

type progressReportRepository struct {
	ReportRepository
	reports map[string]json.RawMessage
	row     ReportRow
}

/** reportKey combines the kind and key used by the in-memory issue store. */
func reportKey(kind, key string) string {
	return kind + ":" + key
}

/** ReportKeys returns saved issue keys in chronological order. */
func (r *progressReportRepository) ReportKeys(_ context.Context, kind string) ([]string, error) {
	keys := make([]string, 0)
	for combined := range r.reports {
		savedKind, key, ok := strings.Cut(combined, ":")
		if ok && savedKind == kind {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

/** SavedReport reports whether the requested issue is already stored. */
func (r *progressReportRepository) SavedReport(_ context.Context, kind, key string) (*SavedReport, error) {
	if _, ok := r.reports[reportKey(kind, key)]; !ok {
		return nil, nil
	}
	return &SavedReport{Entries: 1}, nil
}

/** DailyIssues returns saved issues within the requested report period. */
func (r *progressReportRepository) DailyIssues(ctx context.Context, startDate, endDate string) ([]Issue, error) {
	keys, _ := r.ReportKeys(ctx, "daily")
	issues := make([]Issue, 0)
	for _, key := range keys {
		if key < startDate || key > endDate {
			continue
		}
		issues = append(issues, Issue{
			Key:     key,
			Content: r.reports[reportKey("daily", key)],
		})
	}
	return issues, nil
}

/** DailyMemoryIssues returns earlier issues in the memory window. */
func (r *progressReportRepository) DailyMemoryIssues(ctx context.Context, date, fromDate string) ([]DailyMemoryIssue, error) {
	issues, err := r.DailyIssues(ctx, fromDate, date)
	if err != nil {
		return nil, err
	}
	previous := make([]DailyMemoryIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.Key < date {
			previous = append(previous, DailyMemoryIssue{
				Key:     issue.Key,
				Content: issue.Content,
			})
		}
	}
	return previous, nil
}

/** PeriodReports returns the selected fixture when it falls inside the window. */
func (r *progressReportRepository) PeriodReports(_ context.Context, start, end time.Time) ([]ReportRow, error) {
	if !r.row.TimelineAt.Before(start) && r.row.TimelineAt.Before(end) {
		return []ReportRow{r.row}, nil
	}
	return []ReportRow{}, nil
}

/** CurrentReportsByIDs resolves saved daily references to the current fixture. */
func (r *progressReportRepository) CurrentReportsByIDs(_ context.Context, ids []string, _ bool) ([]ReportRow, error) {
	for _, id := range ids {
		if id == r.row.ID {
			return []ReportRow{r.row}, nil
		}
	}
	return []ReportRow{}, nil
}

/** CurrentFactsByArticles returns no additional fact identity for this fixture. */
func (*progressReportRepository) CurrentFactsByArticles(context.Context, []string) ([]ArticleFact, error) {
	return []ArticleFact{}, nil
}

/** MissedFacts returns no community facts for this fixture. */
func (*progressReportRepository) MissedFacts(context.Context, time.Time, time.Time, interests.PublicationSettings) ([]int64, error) {
	return []int64{}, nil
}

/** ReportsForFacts returns no additional reports for this fixture. */
func (*progressReportRepository) ReportsForFacts(context.Context, []int64, time.Time, interests.PublicationSettings) ([]ReportRow, error) {
	return []ReportRow{}, nil
}

/** FactSources returns no extra sources for this fixture. */
func (*progressReportRepository) FactSources(context.Context, []int64, time.Time) (map[int64][]string, error) {
	return map[int64][]string{}, nil
}

/** StoryParticipants returns no external participants for this fixture. */
func (*progressReportRepository) StoryParticipants(context.Context, []int64, time.Time, time.Time, interests.PublicationSettings) (map[int64]int, error) {
	return map[int64]int{}, nil
}

/** MentionedStories returns no story mentions for this fixture. */
func (*progressReportRepository) MentionedStories(context.Context, []string) ([]Mention, error) {
	return []Mention{}, nil
}

/** SaveReport stores the composed issue in the in-memory repository. */
func (r *progressReportRepository) SaveReport(_ context.Context, kind, key string, _, _ time.Time, content map[string]any, _ *string, _ *string, _ []int64) (bool, error) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return false, err
	}
	r.reports[reportKey(kind, key)] = encoded
	return true, nil
}

type retryReportRepository struct {
	ReportRepository
	row          ReportRow
	issue        Issue
	failFirst    bool
	saved        bool
	saveAttempts [][]int64
}

/** SavedReport exposes the report after the simulated commit succeeds. */
func (r *retryReportRepository) SavedReport(context.Context, string, string) (*SavedReport, error) {
	if !r.saved {
		return nil, nil
	}
	return &SavedReport{Entries: 1}, nil
}

/** DailyIssues provides the carried item used by the period report. */
func (r *retryReportRepository) DailyIssues(context.Context, string, string) ([]Issue, error) {
	return []Issue{r.issue}, nil
}

/** CurrentReportsByIDs resolves the carried item to its current publication. */
func (r *retryReportRepository) CurrentReportsByIDs(context.Context, []string, bool) ([]ReportRow, error) {
	return []ReportRow{r.row}, nil
}

/** PeriodReports provides the report candidates for the issue window. */
func (r *retryReportRepository) PeriodReports(context.Context, time.Time, time.Time) ([]ReportRow, error) {
	return []ReportRow{r.row}, nil
}

/** SaveReport records receipt IDs and simulates a failed transaction before recovery. */
func (r *retryReportRepository) SaveReport(_ context.Context, _, _ string, _, _ time.Time, _ map[string]any, _ *string, _ *string, receiptIDs []int64) (bool, error) {
	r.saveAttempts = append(r.saveAttempts, append([]int64(nil), receiptIDs...))
	if r.failFirst {
		r.failFirst = false
		return false, errors.New("receipt commit interrupted")
	}
	r.saved = true
	return true, nil
}

type retryReportModel struct {
	calls       int
	providerHit int
}

/** ResolveKey returns the fixed model used by this recovery fixture. */
func (*retryReportModel) ResolveKey(context.Context, string) (string, error) {
	return "fixture-model", nil
}

/** Call returns one received response and reuses it on the next report attempt. */
func (m *retryReportModel) Call(context.Context, modelcall.Request) (modelcall.Result, error) {
	m.calls++
	if m.calls == 1 {
		m.providerHit++
	}
	return modelcall.Result{
		Model:      "fixture-model",
		ReceiptID:  73,
		Reused:     m.calls > 1,
		Completion: provider.Completion{Text: `{"overview":"OpenAI 发布 GPT-5。","sections":{}}`},
	}, nil
}

/** Reject is unused because the fixture returns a valid report response. */
func (*retryReportModel) Reject(context.Context, int64, string) error {
	return nil
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
	files, err := readPromptFiles("../../../backend-go/config/prompts")
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

/** Verifies empty older days do not prevent later daily and period issues from saving. */
func TestComposeDueContinuesPastEmptyIssueGaps(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	category := "industry"
	score := 90
	summary := "OpenAI 发布 GPT-5。"
	row := ReportRow{
		ID: "recovery-item", Title: "OpenAI 发布 GPT-5", Summary: &summary, URL: "https://example.invalid/recovery",
		Category: &category, Score: &score, SourceID: "recovery-source", SourceName: "Example", SourceKind: "rss", SourceTier: "T1",
		TimelineAt: time.Date(2024, time.February, 1, 12, 0, 0, 0, time.UTC),
	}
	repository := &progressReportRepository{
		reports: map[string]json.RawMessage{
			reportKey("daily", "2024-01-23"): json.RawMessage(`{"leadItemId":"recovery-item","sections":[{"items":[{"itemId":"recovery-item","title":"OpenAI 发布 GPT-5","sources":1}]}]}`),
		},
		row: row,
	}
	service, err := NewService(repository, nil, Config{
		SiteName: "Example", DailyTime: "08:00", WeeklyTime: "10:00", MonthlyTime: "10:30",
		Categories: []Category{{
			Key:     category,
			Section: "行业动态",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ComposeDue(context.Background(), time.Date(2024, time.February, 2, 9, 0, 0, 0, location), 100)
	if err == nil || !strings.Contains(err.Error(), "reports:") {
		t.Fatalf("compose due error = %v", err)
	}
	generated, ok := result["generated"].([]string)
	if !ok {
		t.Fatalf("generated result = %#v", result["generated"])
	}
	generatedSet := make(map[string]struct{}, len(generated))
	for _, key := range generated {
		generatedSet[key] = struct{}{}
	}
	for _, want := range []string{"daily:2024-02-02", "weekly:2024-W04", "monthly:2024-01"} {
		if _, exists := generatedSet[want]; !exists {
			t.Errorf("later issue %s was not generated; got %v", want, generated)
		}
	}
	if _, exists := generatedSet["daily:2024-01-24"]; exists {
		t.Fatal("an empty daily gap was reported as generated")
	}
}

/** Verifies a report retry reuses its received model response after commit failure. */
func TestPeriodReportRetryReusesReceivedModelResponse(t *testing.T) {
	prompts, err := readPromptFiles("../../../backend-go/config/prompts")
	if err != nil {
		t.Fatal(err)
	}
	category := "industry"
	score := 90
	summary := "OpenAI 发布 GPT-5。"
	row := ReportRow{
		ID: "report-retry-item", Title: "OpenAI 发布 GPT-5", Summary: &summary, URL: "https://example.invalid/report",
		Category: &category, Score: &score, SourceID: "report-retry-source", SourceName: "Example", SourceKind: "rss", SourceTier: "T1",
		TimelineAt: time.Date(2024, time.February, 2, 0, 0, 0, 0, time.UTC),
	}
	repository := &retryReportRepository{
		row: row,
		issue: Issue{
			Key:     "2024-02-02",
			Content: json.RawMessage(`{"leadItemId":"report-retry-item","sections":[{"items":[{"itemId":"report-retry-item","sources":1}]}]}`),
		},
		failFirst: true,
	}
	models := &retryReportModel{}
	service, err := NewService(repository, models, Config{
		SiteName: "Example", DailyTime: "08:00", WeeklyTime: "10:00", MonthlyTime: "10:30",
		Categories: []Category{{
			Key:     category,
			Section: "行业动态",
		}},
		Entities: map[string]interests.Entity{
			"openai": {Name: "OpenAI"},
		},
		Prompts: prompts,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.ComposeWeekly(context.Background(), "2024-W05", nil); err == nil || !strings.Contains(err.Error(), "receipt commit interrupted") {
		t.Fatalf("first report save error = %v", err)
	}
	result, err := service.ComposeWeekly(context.Background(), "2024-W05", nil)
	if err != nil || result["key"] != "2024-W05" || !repository.saved {
		t.Fatalf("recovered report = %#v, saved=%t, error=%v", result, repository.saved, err)
	}
	if models.calls != 2 || models.providerHit != 1 || len(repository.saveAttempts) != 2 {
		t.Fatalf("model calls=%d provider hits=%d saves=%d", models.calls, models.providerHit, len(repository.saveAttempts))
	}
	for _, receiptIDs := range repository.saveAttempts {
		if !reflect.DeepEqual(receiptIDs, []int64{73}) {
			t.Errorf("report save receipt IDs = %v", receiptIDs)
		}
	}
	if _, err := service.ComposeWeekly(context.Background(), "2024-W05", nil); err != nil || models.calls != 2 || len(repository.saveAttempts) != 2 {
		t.Fatalf("published report retry called model or wrote again: calls=%d saves=%d error=%v", models.calls, len(repository.saveAttempts), err)
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

/** TestDailyMetricsCountOnlyNewOfficialModelLaunches preserves the industry release rule. */
func TestDailyMetricsCountOnlyNewOfficialModelLaunches(t *testing.T) {
	category := "ai-models"
	otherCategory := "ai-products"
	previous := PreviousStory{Key: "2026-10-01", Title: "Already reported"}
	base := EditionEntry{
		Entry: Entry{
			ItemID:     "release",
			SourceID:   "official",
			FirstParty: true,
			Related:    []RelatedEntry{{SourceID: "coverage"}},
		},
		Category:  &category,
		Tags:      []string{"模型发布"},
		Authority: 0,
	}
	entries := []EditionEntry{
		base,
		{Entry: base.Entry, Category: &category, Tags: []string{"评测/基准"}},
		{Entry: base.Entry, Category: &category, Tags: []string{"产品更新"}},
		{Entry: base.Entry, Category: &otherCategory, Tags: base.Tags},
		{Entry: base.Entry, Category: &category, Tags: base.Tags, Authority: 3},
		{Entry: base.Entry, Category: &category, Tags: base.Tags, Previous: &previous},
		{Entry: base.Entry, Category: &category},
	}
	service := &Service{config: Config{Reporting: ReportingConfig{Release: &Release{Category: "ai-models", Tag: "模型发布"}}}}
	metrics := service.dailyMetrics(entries)
	if metrics["totalEvents"] != 7 || metrics["modelsReleased"] != 1 || metrics["sourcesCount"] != 2 || metrics["firstPartyEvents"] != 7 {
		t.Fatalf("daily metrics = %#v", metrics)
	}
	service.config.Reporting.Release = nil
	withoutRelease := service.dailyMetrics(entries)
	if _, exists := withoutRelease["modelsReleased"]; exists {
		t.Fatalf("industry without a release rule exposed a release metric: %#v", withoutRelease)
	}
}
