package publiccontent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type v1ReportsFakeRepository struct {
	*fakeRepository
	index         map[string][]V1ReportIndexRecord
	reports       map[string]*V1ReportRecord
	feedReports   map[string][]V1ReportRecord
	availability  map[string]V1ReportAvailabilityRecord
	replacements  map[string]*string
	requestedKind string
	requestedKey  string
	latest        bool
	issueNumber   int
	previous      *string
	next          *string
	cover         *SiteReportCoverRecord
}

func (r *v1ReportsFakeRepository) SiteReportIssueNumber(_ context.Context, kind, key string) (int, error) {
	if r.issueNumber != 0 {
		return r.issueNumber, nil
	}
	for _, row := range r.index[kind] {
		if row.Key == key {
			return row.IssueNumber, nil
		}
	}
	return 1, nil
}

func (r *v1ReportsFakeRepository) SiteReportNeighbors(_ context.Context, _, _ string) (*string, *string, error) {
	return r.previous, r.next, nil
}

func (r *v1ReportsFakeRepository) SiteReportCover(_ context.Context, _ string, _ time.Time) (*SiteReportCoverRecord, error) {
	return r.cover, nil
}

func (r *v1ReportsFakeRepository) V1FeedReports(_ context.Context, kind string, limit int) ([]V1ReportRecord, error) {
	rows := r.feedReports[kind]
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r *v1ReportsFakeRepository) V1ReportIndex(_ context.Context, kind string, limit int) ([]V1ReportIndexRecord, error) {
	r.requestedKind = kind
	rows := r.index[kind]
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r *v1ReportsFakeRepository) V1Report(_ context.Context, kind, key string, latest bool) (*V1ReportRecord, error) {
	r.requestedKind, r.requestedKey, r.latest = kind, key, latest
	if latest {
		return r.reports[kind+":latest"], nil
	}
	return r.reports[kind+":"+key], nil
}

func (r *v1ReportsFakeRepository) V1ReportAvailability(_ context.Context, ids []string, _ time.Time) (map[string]V1ReportAvailabilityRecord, error) {
	result := make(map[string]V1ReportAvailabilityRecord)
	for _, id := range ids {
		if item, ok := r.availability[id]; ok {
			result[id] = item
		}
	}
	return result, nil
}

func (r *v1ReportsFakeRepository) V1DailyReplacementSummaries(_ context.Context, replacements []V1DailyReplacement) (map[string]*string, error) {
	result := make(map[string]*string)
	for _, replacement := range replacements {
		result[replacement.Key+"\x00"+replacement.ItemID] = r.replacements[replacement.ItemID]
	}
	return result, nil
}

func TestSiteReportIndexPreservesEntriesAndReplacesWithdrawnHeadline(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{},
		index: map[string][]V1ReportIndexRecord{"daily": {{
			Key: "2026-10-07", IssueNumber: 42, GeneratedAt: now,
			Content: map[string]any{
				"leadItemId": "withdrawn", "highlights": []any{"replacement"},
				"sections": []any{map[string]any{"items": []any{
					map[string]any{"itemId": "withdrawn", "title": "Withdrawn title"},
					map[string]any{"itemId": "replacement", "title": "Replacement headline"},
				}}},
			},
		}}},
		availability: map[string]V1ReportAvailabilityRecord{
			"withdrawn":   {Available: false},
			"replacement": {Available: true},
		},
	}
	response, err := NewService(repository).SiteReportIndex(context.Background(), "daily", now, "Example")
	if err != nil {
		t.Fatal(err)
	}
	if response.Kind != "daily" || len(response.Items) != 1 {
		t.Fatalf("response = %#v", response)
	}
	item := response.Items[0]
	if item.Key != "2026-10-07" || item.IssueNumber != 42 || item.Title == nil || *item.Title != "Replacement headline" || item.Count != 2 {
		t.Fatalf("site report entry = %#v", item)
	}
	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	entry := decoded["items"].([]any)[0].(map[string]any)
	if decoded["kind"] != "daily" || entry["issueNumber"] != float64(42) || entry["title"] != "Replacement headline" || entry["count"] != float64(2) {
		t.Fatalf("wire response = %s", wire)
	}
}

func TestV1DailyWithAgentNotesKeepsOnlyAvailableRelatedEntries(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	report := &V1ReportRecord{Kind: "daily", Key: "2026-10-07", WindowStart: now.Add(-24 * time.Hour), WindowEnd: now, GeneratedAt: now, Content: map[string]any{
		"sections": []any{map[string]any{"label": "模型", "items": []any{
			map[string]any{
				"itemId": "a1", "title": "Article", "summary": "Summary", "sourceName": "Source", "sourceUrl": "https://source.invalid/a1",
				"sources": float64(3), "followUp": "2026-10-01", "related": []any{
					map[string]any{"itemId": "a2", "title": "Related"},
					map[string]any{"itemId": "gone", "title": "Withdrawn"},
				},
			},
			map[string]any{"title": "No item id", "sourceUrl": "https://source.invalid/no-id", "followUp": ""},
		}}},
		"flashes": []any{},
	}}
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{},
		reports: map[string]*V1ReportRecord{
			"daily:latest":     report,
			"daily:2026-10-07": report,
		},
		availability: map[string]V1ReportAvailabilityRecord{
			"a1":   {Available: true, SourceName: stringPointer("Current source")},
			"a2":   {Available: true},
			"gone": {Available: false},
		},
	}
	response, notes, err := NewService(repository).V1DailyWithAgentNotes(context.Background(), "latest", true, now, "https://site.invalid/", "Example")
	if err != nil || response == nil {
		t.Fatalf("response=%#v notes=%#v error=%v", response, notes, err)
	}
	if repository.requestedKind != "daily" || repository.requestedKey != "2026-10-07" || repository.latest {
		t.Fatalf("note lookup should address the rendered report key, request=%s/%s latest=%v", repository.requestedKind, repository.requestedKey, repository.latest)
	}
	note, ok := notes["https://site.invalid/items/a1"]
	if !ok || note.OtherSources != 2 || note.FollowUp == nil || *note.FollowUp != "2026-10-01" || len(note.Related) != 1 || note.Related[0] != (V1DailyAgentRelated{Title: "Related", Link: "https://site.invalid/items/a2"}) {
		t.Fatalf("article notes = %#v", note)
	}
	if _, found := notes["https://source.invalid/no-id"]; found {
		t.Fatal("empty follow-up must not create an agent note")
	}
}

func TestSiteReportIndexRejectsUnsupportedKind(t *testing.T) {
	if _, err := NewService(&v1ReportsFakeRepository{fakeRepository: &fakeRepository{}}).SiteReportIndex(context.Background(), "quarterly", time.Now(), "Example"); err == nil {
		t.Fatal("unsupported kind was accepted")
	}
}

func TestSiteReportNavigationAndMonthKeepSelectiveTitlesAndIssueNumbers(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	records := make([]V1ReportIndexRecord, 0, 5)
	for i := 0; i < 5; i++ {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		records = append(records, V1ReportIndexRecord{
			Key: day, IssueNumber: 5 - i, GeneratedAt: now,
			Content: map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"itemId": day, "title": "Headline " + day}}}}},
		})
	}
	service := NewService(&v1ReportsFakeRepository{fakeRepository: &fakeRepository{}, index: map[string][]V1ReportIndexRecord{"daily": records}})
	navigation, err := service.SiteReportNavigation(context.Background(), "daily", "2026-09-30", now, "Example")
	if err != nil {
		t.Fatal(err)
	}
	if len(navigation.Items) != 5 {
		t.Fatalf("navigation = %#v", navigation)
	}
	for i, item := range navigation.Items {
		if item.titleIncluded != (i < 3) {
			t.Errorf("daily navigation item %d titleIncluded=%v", i, item.titleIncluded)
		}
		if item.IssueNumber != 5-i {
			t.Errorf("daily issue %s number=%d", item.Key, item.IssueNumber)
		}
	}
	wire, err := json.Marshal(navigation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), `"title":null`) || strings.Count(string(wire), `"title":`) != 3 {
		t.Fatalf("sparse navigation title fields = %s", wire)
	}
	month, err := service.SiteDailyReportMonth(context.Background(), "2026-10", now, "Example")
	if err != nil {
		t.Fatal(err)
	}
	if len(month.Items) != 5 {
		t.Fatalf("month items = %#v", month.Items)
	}
	monthWire, err := json.Marshal(month)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(monthWire), `"title":`) != 5 {
		t.Fatalf("month title fields = %s", monthWire)
	}
}

func TestV1DailyIndexAdvancesWithdrawnLeadAndRestoresSummary(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	paragraph := "Replacement summary"
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{},
		index: map[string][]V1ReportIndexRecord{"daily": {{
			Key: "2026-10-07", GeneratedAt: now,
			Content: map[string]any{
				"leadItemId": "withdrawn", "highlights": []any{"replacement"},
				"lead": map[string]any{"title": "Written headline", "leadParagraph": "Original paragraph"},
				"sections": []any{map[string]any{"items": []any{
					map[string]any{"itemId": "withdrawn", "title": "Withdrawn item"},
					map[string]any{"itemId": "replacement", "title": "Replacement item"},
				}}},
			},
		}}},
		availability: map[string]V1ReportAvailabilityRecord{"withdrawn": {Available: false}, "replacement": {Available: true}},
		replacements: map[string]*string{"replacement": &paragraph},
	}
	service := NewService(repository)
	response, err := service.V1Dailies(context.Background(), 30, now, "https://myhot.example/", "Example")
	if err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != 1 || response.Count != 1 || len(response.Items) != 1 {
		t.Fatalf("index shape = %#v", response)
	}
	item := response.Items[0]
	title, titleOK := item["leadTitle"].(*string)
	leadParagraph, paragraphOK := item["leadParagraph"].(*string)
	if item["date"] != "2026-10-07" || !titleOK || *title != "Replacement item" || !paragraphOK || *leadParagraph != paragraph {
		t.Fatalf("withdrawn lead was not replaced in index: %#v", item)
	}
	links := item["links"].(V1ReportLink)
	if links.AIHot != "https://myhot.example/daily/2026-10-07" {
		t.Fatalf("report link = %q", links.AIHot)
	}
}

func TestV1DailyAndPeriodDetailsKeepWireFieldsAndWithdrawnFiltering(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	generated := now.Add(-time.Hour)
	content := map[string]any{
		"leadItemId": "gone", "highlights": []any{"kept"}, "lead": map[string]any{"title": "Headline", "leadParagraph": "Written lead"},
		"sections": []any{map[string]any{"label": "Industry", "items": []any{
			map[string]any{"itemId": "gone", "title": "Withdrawn title", "summary": "must not appear", "sourceName": "Old", "sourceUrl": "https://fixture.example/gone"},
			map[string]any{"itemId": "kept", "title": "Kept title", "summary": "Frozen summary", "sourceName": "Frozen source", "sourceUrl": "https://fixture.example/kept"},
		}}},
		"flashes": []any{map[string]any{"itemId": "kept", "title": "Flash", "sourceName": "Frozen source", "sourceUrl": "https://fixture.example/flash"}},
	}
	weekly := map[string]any{
		"headline": "Example 周报 · 2026-W41", "overview": "Stored overview",
		"themes": []any{map[string]any{"heading": "Theme", "summary": "Theme summary", "storyRefs": []any{
			map[string]any{"itemId": "gone", "title": "Withdrawn title", "summary": "gone summary"},
			map[string]any{"itemId": "kept", "title": "Kept title", "summary": "Frozen summary", "sourceName": "Frozen source", "sourceUrl": "https://fixture.example/kept", "publishedAt": generated.Format(time.RFC3339Nano)},
		}}},
	}
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{},
		reports: map[string]*V1ReportRecord{
			"daily:2026-10-07": {Kind: "daily", Key: "2026-10-07", WindowStart: generated.Add(-24 * time.Hour), WindowEnd: generated, Content: content, GeneratedAt: generated},
			"weekly:2026-W41":  {Kind: "weekly", Key: "2026-W41", WindowStart: generated.Add(-7 * 24 * time.Hour), WindowEnd: generated, Content: weekly, GeneratedAt: generated},
		},
		availability: map[string]V1ReportAvailabilityRecord{
			"gone": {Available: false},
			"kept": {Available: true, Summary: stringPtr("Current summary"), SourceName: stringPtr("Current source"), SourceID: "source-1", PublishedAt: timePointer(generated)},
		},
	}
	service := NewService(repository)
	daily, err := service.V1Daily(context.Background(), "2026-10-07", false, now, "https://myhot.example", "Example")
	if err != nil || daily == nil {
		t.Fatalf("daily=%#v err=%v", daily, err)
	}
	if daily.Report.Lead == nil || daily.Report.Lead.Title != "Kept title" || daily.Report.Lead.LeadParagraph != "Frozen summary" || len(daily.Report.Sections) != 1 || len(daily.Report.Sections[0].Items) != 1 {
		t.Fatalf("withdrawal filtering or replacement lead failed: %#v", daily.Report)
	}
	if daily.Report.Sections[0].Items[0].Summary != "Frozen summary" || daily.Report.Sections[0].Items[0].Source.Name != "Current source" {
		t.Fatalf("citation summary/source metadata mismatch: %#v", daily.Report.Sections[0].Items[0])
	}
	if daily.Report.Sections[0].Items[0].Attribution.Name != "Example" || daily.Report.Sections[0].Items[0].Attribution.URL != "https://myhot.example/items/kept" {
		t.Fatalf("citation attribution = %#v", daily.Report.Sections[0].Items[0].Attribution)
	}
	encoded, err := json.Marshal(daily)
	if err != nil {
		t.Fatal(err)
	}
	var dailyWire map[string]any
	if err := json.Unmarshal(encoded, &dailyWire); err != nil {
		t.Fatal(err)
	}
	report := dailyWire["report"].(map[string]any)
	if report["lead"] == nil || len(report["flashes"].([]any)) != 1 {
		t.Fatalf("daily wire shape = %s", encoded)
	}

	period, err := service.V1Period(context.Background(), "weekly", "2026-W41", false, now, "https://myhot.example", "Example", "件", "大事")
	if err != nil || period == nil {
		t.Fatalf("period=%#v err=%v", period, err)
	}
	if period.Report.Week == nil || *period.Report.Week != "2026-W41" || period.Report.PeriodStart != "2026-10-05" || period.Report.PeriodEnd != "2026-10-11" {
		t.Fatalf("week range = %#v", period.Report)
	}
	if period.Report.Headline == nil || *period.Report.Headline != "Kept title" || period.Report.Overview == nil || *period.Report.Overview != "本周 1 件大事，最受关注的是：Kept title。" || len(period.Report.Sections) != 1 || period.Report.Sections[0].Summary != nil {
		t.Fatalf("withdrawn period output = %#v", period.Report)
	}
	if repository.requestedKind != "weekly" || repository.requestedKey != "2026-W41" || repository.latest {
		t.Fatalf("requested report = %s %s latest=%v", repository.requestedKind, repository.requestedKey, repository.latest)
	}
}

func TestV1FeedIssuesFilterWithdrawnEntriesAndRefreshDailyLead(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{},
		feedReports: map[string][]V1ReportRecord{"daily": {{
			Kind: "daily", Key: "2026-10-07", GeneratedAt: now,
			Content: map[string]any{
				"leadItemId": "withdrawn", "highlights": []any{"replacement"},
				"lead": map[string]any{"title": "Withdrawn headline", "leadParagraph": "withdrawn paragraph"},
				"sections": []any{map[string]any{"label": "Industry", "items": []any{
					map[string]any{"itemId": "withdrawn", "title": "Withdrawn headline", "summary": "withdrawn paragraph"},
					map[string]any{"itemId": "replacement", "title": "Replacement headline", "summary": "replacement paragraph"},
				}}},
			},
		}}},
		availability: map[string]V1ReportAvailabilityRecord{
			"withdrawn": {Available: false}, "replacement": {Available: true},
		},
	}
	issues, err := NewService(repository).V1FeedIssues(context.Background(), "daily", 30, now, "Example", "件", "大事", "https://myhot.example")
	if err != nil || len(issues) != 1 {
		t.Fatalf("issues=%#v err=%v", issues, err)
	}
	issue := issues[0]
	if issue.Headline == nil || *issue.Headline != "Replacement headline" || issue.LeadParagraph == nil || *issue.LeadParagraph != "replacement paragraph" {
		t.Fatalf("daily lead was not replaced: %#v", issue)
	}
	if len(issue.Sections) != 1 || len(issue.Sections[0].Items) != 1 || issue.Sections[0].Items[0].Title != "Replacement headline" || issue.Sections[0].Items[0].Link != "https://myhot.example/items/replacement" {
		t.Fatalf("withdrawn citation remains in RSS projection: %#v", issue.Sections)
	}
}

func TestReportPeriodRangeValidatesCalendarBoundaries(t *testing.T) {
	for _, test := range []struct {
		kind       string
		key        string
		start, end string
		valid      bool
	}{
		{"weekly", "2026-W41", "2026-10-05", "2026-10-11", true},
		{"weekly", "2021-W53", "", "", false},
		{"weekly", "2020-W53", "2020-12-28", "2021-01-03", true},
		{"monthly", "2024-02", "2024-02-01", "2024-02-29", true},
		{"monthly", "2024-13", "", "", false},
	} {
		start, end, valid := ReportPeriodRange(test.kind, test.key)
		if valid != test.valid || (valid && (start != test.start || end != test.end)) {
			t.Errorf("range %s %s = %q %q %v", test.kind, test.key, start, end, valid)
		}
	}
}

func timePointer(value time.Time) *time.Time { return &value }
