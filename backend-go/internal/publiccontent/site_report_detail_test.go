package publiccontent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSiteReportDetailReplacesWithdrawnLeadAndKeepsPublicPresentation(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	previous, next := "2026-10-06", "2026-10-08"
	image := "https://images.example.invalid/cover.jpg"
	report := &V1ReportRecord{Kind: "daily", Key: "2026-10-07", GeneratedAt: now, Content: map[string]any{
		"leadItemId": "withdrawn", "lead": map[string]any{"title": "Withdrawn editorial lead", "leadParagraph": "Saved paragraph"},
		"overview": "Daily overview", "highlights": []any{"live"},
		"sections": []any{map[string]any{"label": "研究", "items": []any{
			map[string]any{"itemId": "withdrawn", "title": "Withdrawn editorial lead", "summary": "private after withdrawal"},
			map[string]any{"itemId": "live", "title": "Available result", "summary": "Frozen public summary", "sourceName": "Original", "sourceUrl": "https://source.example.invalid/live", "sourceId": "old-source", "sources": 3, "related": []any{map[string]any{"itemId": "related", "title": "Related result", "sourceId": "related-source"}}, "followUp": "2026-10-06"},
		}}},
		"flashes": []any{}, "metrics": map[string]any{"firstPartyEvents": 99, "sourcesCount": 99},
	}}
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{}, issueNumber: 42, previous: &previous, next: &next,
		reports: map[string]*V1ReportRecord{"daily:2026-10-07": report},
		availability: map[string]V1ReportAvailabilityRecord{
			"withdrawn": {Available: false},
			"live":      {Available: true, Summary: stringPointer("Current summary"), FirstParty: true, SourceID: "current-source", SourceName: stringPointer("X: Current (@current)"), SourceIcon: stringPointer("https://images.example.invalid/icon.png"), PublishedAt: timePointer(now)},
			"related":   {Available: true, SourceID: "related-source"},
		},
		cover: &SiteReportCoverRecord{URL: image, Width: floatPointer(1280), Height: floatPointer(720)},
	}
	config := SiteReportConfig{SiteName: "MyHOT", Subject: "AI", ImageProxySecret: "test-secret"}
	result, err := NewService(repository).SiteReport(context.Background(), "daily", report.Key, now, config)
	if err != nil {
		t.Fatal(err)
	}
	if result.IssueNumber != 42 || result.Previous == nil || *result.Previous != previous || result.Next == nil || *result.Next != next {
		t.Fatalf("report position = %#v", result)
	}
	if result.Title != "AI 日报 · 2026-10-07" || result.Lead == nil || result.Lead.Title != "Available result" || result.Lead.LeadParagraph != "Frozen public summary" || result.LeadItemID == nil || *result.LeadItemID != "live" {
		t.Fatalf("report lead = %#v", result)
	}
	if len(result.Sections) != 1 || len(result.Sections[0].Items) != 2 || result.Sections[0].Items[0].Available || result.Sections[0].Items[0].Summary != nil {
		t.Fatalf("withdrawn citation = %#v", result.Sections)
	}
	citation := result.Sections[0].Items[1]
	if citation.Summary == nil || *citation.Summary != "Frozen public summary" || citation.SourceName != "Current" || !citation.FirstParty || citation.SourceIconURL == nil || !strings.HasPrefix(*citation.SourceIconURL, "/api/img-proxy?") || citation.SourceIconSrcSet == nil {
		t.Fatalf("available citation = %#v", citation)
	}
	if citation.OtherSources == nil || *citation.OtherSources != 2 || len(citation.Related) != 1 || citation.Related[0].Summary != nil || citation.FollowUp == nil || *citation.FollowUp != "2026-10-06" {
		t.Fatalf("citation extras = %#v", citation)
	}
	if result.Cover == nil || result.Cover.Caption != nil || result.Cover.Width == nil || *result.Cover.Width != 1280 || !strings.HasPrefix(result.Cover.URL, "/api/img-proxy?") {
		t.Fatalf("report cover = %#v", result.Cover)
	}
	if result.Metrics["firstPartyEvents"] != 1 || result.Metrics["sourcesCount"] != 2 {
		t.Fatalf("recomputed metrics = %#v", result.Metrics)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["leadItemId"] != "live" || decoded["prev"] != previous || decoded["next"] != next {
		t.Fatalf("report JSON contract = %s", wire)
	}
}

func TestSiteReportLatestPageIncludesNavigationAndNewestDetail(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	key := "2026-10-07"
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{},
		index:          map[string][]V1ReportIndexRecord{"daily": {{Key: key, IssueNumber: 7, GeneratedAt: now, Content: map[string]any{"sections": []any{}}}}},
		reports:        map[string]*V1ReportRecord{"daily:" + key: {Kind: "daily", Key: key, GeneratedAt: now, Content: map[string]any{"sections": []any{}, "flashes": []any{}, "metrics": map[string]any{}}}},
	}
	page, err := NewService(repository).SiteReportLatestPage(context.Background(), "daily", now, SiteReportConfig{SiteName: "MyHOT", Subject: "AI"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Report == nil || page.Report.Key != key || len(page.Index) != 1 || page.Index[0].Key != key || page.Index[0].IssueNumber != 7 {
		t.Fatalf("latest page = %#v", page)
	}

	empty := &v1ReportsFakeRepository{fakeRepository: &fakeRepository{}, index: map[string][]V1ReportIndexRecord{"weekly": {}}}
	page, err = NewService(empty).SiteReportLatestPage(context.Background(), "weekly", now, SiteReportConfig{SiteName: "MyHOT"})
	if err != nil || page.Report != nil || page.Index == nil || len(page.Index) != 0 {
		t.Fatalf("empty latest page = %#v, err=%v", page, err)
	}
}

func TestSiteReportDoesNotReturnCoverForUnavailableLead(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	report := &V1ReportRecord{Kind: "daily", Key: "2026-10-07", GeneratedAt: now, Content: map[string]any{
		"leadItemId": "gone", "lead": map[string]any{"title": "Lead"}, "sections": []any{map[string]any{"items": []any{map[string]any{"itemId": "gone", "title": "Lead"}}}},
	}}
	repository := &v1ReportsFakeRepository{
		fakeRepository: &fakeRepository{}, reports: map[string]*V1ReportRecord{"daily:2026-10-07": report},
		availability: map[string]V1ReportAvailabilityRecord{"gone": {Available: false}},
		cover:        &SiteReportCoverRecord{URL: "https://images.example.invalid/must-not-appear.jpg"},
	}
	result, err := NewService(repository).SiteReport(context.Background(), "daily", report.Key, now, SiteReportConfig{ImageProxySecret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cover != nil || result.LeadItemID != nil || result.Sections[0].Items[0].Available {
		t.Fatalf("unavailable lead leaked to cover or item link: %#v", result)
	}
}

func floatPointer(value float64) *float64 { return &value }
